package volumehelper_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/hostenv"
)

// formatVolumesScript is the shared volume-formatting helper whose
// ext4_label function this file exercises against real ext4 images. It
// must not depend on e2label (see CheckPrereqs in volumehelper.go and
// docs/guide/volumes.md): e2label lives in e2fsprogs-extra, not the
// plain e2fsprogs package that provides mkfs.ext4, so an image such as
// Alpine's - e2fsprogs only - won't have it.
const formatVolumesScript = "../../support/volumes-support/base/usr/lib/contemper/format-volumes"

// extractFunction pulls just the named shell function out of
// formatVolumesScript, so a test runs the real function the image ships
// (not a copy hand-maintained here) without also running the script's
// own main loop, which would read /etc/contemper/volumes and exit before
// a test could call the function at all.
func extractFunction(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile(formatVolumesScript)
	if err != nil {
		t.Fatalf("reading %s: %v", formatVolumesScript, err)
	}
	re := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `\(\) \{.*?^\}$`)
	fn := re.Find(src)
	if fn == nil {
		t.Fatalf("%s() function not found in %s", name, formatVolumesScript)
	}
	return string(fn)
}

// extractExt4Label pulls just the ext4_label() function out of
// formatVolumesScript; see extractFunction.
func extractExt4Label(t *testing.T) string {
	t.Helper()
	return extractFunction(t, "ext4_label")
}

// runExt4Label runs the real ext4_label() function against device/file
// imgPath, using the interpreter at shellPath, and returns its result
// and whether it exited 0. It captures ext4_label's own output through
// a command substitution ("label=$(ext4_label ...)"), exactly like
// format-volumes' own "existing_label=$(ext4_label "$dev")" does: that
// is what turns a NUL-padded s_volume_name into a clean, correctly
// truncated string (a shell variable can't hold an embedded NUL, so
// capturing through $() truncates at the first one, the same way a C
// string would) - calling ext4_label directly and reading its raw
// stdout would see the padding bytes as-is, unlike real usage.
func runExt4Label(t *testing.T, shellPath, fn, imgPath string) (label string, ok bool) {
	t.Helper()
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.sh")
	script := fn + "\n" +
		"label=$(ext4_label \"$1\")\n" +
		"rc=$?\n" +
		"printf '%s' \"$label\"\n" +
		"exit \"$rc\"\n"
	if err := os.WriteFile(runner, []byte(script), 0o755); err != nil {
		t.Fatalf("writing runner script: %v", err)
	}
	out, err := exec.Command(shellPath, runner, imgPath).Output()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running %s %s: %v", shellPath, runner, err)
		}
		return strings.TrimRight(string(out), "\n"), false
	}
	return strings.TrimRight(string(out), "\n"), true
}

// makeExt4Image creates a small sparse ext4 image at dir/name.img,
// labelled label, using mkfsPath (mkfs.ext4).
func makeExt4Image(t *testing.T, mkfsPath, dir, name, label string) string {
	t.Helper()
	img := filepath.Join(dir, name+".img")
	f, err := os.Create(img)
	if err != nil {
		t.Fatalf("creating %s: %v", img, err)
	}
	const size = 4 * 1024 * 1024
	if err := f.Truncate(size); err != nil {
		f.Close()
		t.Fatalf("truncating %s: %v", img, err)
	}
	f.Close()

	out, err := exec.Command(mkfsPath, "-q", "-F", "-L", label, img).CombinedOutput()
	if err != nil {
		t.Fatalf("mkfs.ext4 -L %q %s: %v\n%s", label, img, err, out)
	}
	return img
}

// TestExt4LabelRealImages exercises format-volumes' ext4_label()
// function (which reads the ext2/3/4 superblock directly with dd and
// od, rather than shelling out to e2label - see the comment on
// ext4_label in the script) against images built with the real
// mkfs.ext4, and against non-ext4 data, under every POSIX shell
// available: the host's /bin/sh (dash on Linux, a POSIX-mode shell on
// macOS) and, if one can be found or downloaded, busybox's ash/od/dd,
// since that's the environment the helper actually runs in on an
// Alpine guest.
func TestExt4LabelRealImages(t *testing.T) {
	mkfsPath := hostenv.Find("mkfs.ext4")
	if mkfsPath == "" {
		t.Skip("mkfs.ext4 not found; skipping ext4_label test")
	}
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found; skipping ext4_label test")
	}

	fn := extractExt4Label(t)
	dir := t.TempDir()

	dataImg := makeExt4Image(t, mkfsPath, dir, "data", "data")
	otherImg := makeExt4Image(t, mkfsPath, dir, "other", "other")
	full16Img := makeExt4Image(t, mkfsPath, dir, "full16", "0123456789ABCDEF")

	zeroImg := filepath.Join(dir, "zero.img")
	if err := os.WriteFile(zeroImg, make([]byte, 4*1024*1024), 0o644); err != nil {
		t.Fatalf("writing %s: %v", zeroImg, err)
	}

	nonExtImg := filepath.Join(dir, "nonext.img")
	nonExtData := make([]byte, 4*1024*1024)
	for i := range nonExtData {
		// Deterministic non-zero, non-ext4-superblock filler: anything
		// but 0x00 everywhere and anything but 0xEF53 at the magic
		// offset is fine here.
		nonExtData[i] = byte(0x5a ^ (i % 251))
	}
	if err := os.WriteFile(nonExtImg, nonExtData, 0o644); err != nil {
		t.Fatalf("writing %s: %v", nonExtImg, err)
	}

	cases := []struct {
		name      string
		img       string
		wantOK    bool
		wantLabel string
	}{
		{"matching label", dataImg, true, "data"},
		{"different label", otherImg, true, "other"},
		{"16-character label, no NUL padding", full16Img, true, "0123456789ABCDEF"},
		{"all-zero file, not ext4", zeroImg, false, ""},
		{"non-ext data", nonExtImg, false, ""},
	}

	runWith := func(t *testing.T, shell string) {
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				label, ok := runExt4Label(t, shell, fn, c.img)
				if ok != c.wantOK {
					t.Fatalf("ext4_label(%s): ok = %v, want %v (label=%q)", c.img, ok, c.wantOK, label)
				}
				if ok && label != c.wantLabel {
					t.Errorf("ext4_label(%s) = %q, want %q", c.img, label, c.wantLabel)
				}
			})
		}
	}

	t.Run("sh", func(t *testing.T) {
		runWith(t, shellPath)
	})

	t.Run("busybox", func(t *testing.T) {
		busyboxPath, err := exec.LookPath("busybox")
		if err != nil {
			t.Skip("busybox not found on PATH; skipping the busybox od/dd/sh variant of ext4_label")
		}
		// busybox is a multi-call binary; "busybox sh SCRIPT ARGS..."
		// runs the runner script under busybox's own ash, so its od and
		// dd applets - not the host's coreutils - do the work.
		wrapper := filepath.Join(t.TempDir(), "busybox-sh")
		if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \""+busyboxPath+"\" sh \"$@\"\n"), 0o755); err != nil {
			t.Fatalf("writing busybox sh wrapper: %v", err)
		}
		runWith(t, wrapper)
	})
}

// fakeUdevadm writes a shim to binDir/udevadm that appends each
// invocation's arguments as one line to logPath and exits with
// exitCode, so a test can drive reprobe_disk() through a fake udevadm
// and inspect exactly what it ran without needing real udev.
func fakeUdevadm(t *testing.T, binDir, logPath string, exitCode int) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >>\"%s\"\nexit %d\n", logPath, exitCode)
	if err := os.WriteFile(filepath.Join(binDir, "udevadm"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake udevadm: %v", err)
	}
}

// runReprobeDisk runs the real reprobe_disk() function (plus log(),
// which it calls to report a failing udevadm) against dev/label, with
// PATH set to path, and reports whether it exited 0 - which, per its
// "never fails the boot" contract, it always must.
func runReprobeDisk(t *testing.T, path, dev, label string) (exitOK bool) {
	t.Helper()
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner.sh")
	script := "PROG=contemper-volumes\n" +
		extractFunction(t, "log") + "\n" +
		extractFunction(t, "reprobe_disk") + "\n" +
		"reprobe_disk \"$1\" \"$2\"\n"
	if err := os.WriteFile(runner, []byte(script), 0o755); err != nil {
		t.Fatalf("writing runner script: %v", err)
	}

	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+path)

	cmd := exec.Command("sh", runner, dev, label)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("running reprobe_disk: %v\n%s", err, out)
		}
		return false
	}
	return true
}

// readLines reads path and returns its non-empty lines, or nil if the
// file doesn't exist (the fake udevadm was never invoked).
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading %s: %v", path, err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestReprobeDiskTriggersUdevadmAfterFormat checks that reprobe_disk -
// what format-volumes calls right after a successful mkfs.ext4, to close
// the udev "watch" race described in its own comment and in
// docs/guide/volumes.md - runs exactly "udevadm trigger --action=change
// <dev>" followed by "udevadm settle ... --exit-if-exists=/dev/disk/by-label/<label>".
func TestReprobeDiskTriggersUdevadmAfterFormat(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "udevadm.log")
	fakeUdevadm(t, binDir, logPath, 0)

	path := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if !runReprobeDisk(t, path, "/dev/vdb", "data") {
		t.Fatal("reprobe_disk exited nonzero with a succeeding udevadm")
	}

	lines := readLines(t, logPath)
	want := []string{
		"trigger --action=change /dev/vdb",
		"settle --timeout=10 --exit-if-exists=/dev/disk/by-label/data",
	}
	if len(lines) != len(want) {
		t.Fatalf("udevadm invocations = %v, want %v", lines, want)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("udevadm invocation %d = %q, want %q", i, lines[i], w)
		}
	}
}

// TestReprobeDiskFailingUdevadmNeverFailsBoot checks that reprobe_disk
// still exits 0 - and so never fails format-volumes, which must never
// fail boot - even when both udevadm calls fail.
func TestReprobeDiskFailingUdevadmNeverFailsBoot(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "udevadm.log")
	fakeUdevadm(t, binDir, logPath, 1)

	path := binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if !runReprobeDisk(t, path, "/dev/vdb", "data") {
		t.Fatal("reprobe_disk must exit 0 even when udevadm fails")
	}

	// Both calls should still have been attempted.
	if lines := readLines(t, logPath); len(lines) != 2 {
		t.Errorf("expected both udevadm calls to be attempted despite failure, got %v", lines)
	}
}

// TestReprobeDiskNoUdevadmIsNoop checks the busybox/mdev case (Alpine):
// without udevadm on PATH at all, reprobe_disk does nothing and still
// exits 0, per its own comment and docs/guide/volumes.md.
func TestReprobeDiskNoUdevadmIsNoop(t *testing.T) {
	emptyBin := t.TempDir()
	if !runReprobeDisk(t, emptyBin, "/dev/vdb", "data") {
		t.Fatal("reprobe_disk must exit 0 when udevadm isn't installed")
	}
}

// TestReprobeDiskOnlyCalledAfterFormatting statically checks
// format-volumes' main loop: reprobe_disk must be called exactly once,
// right after a successful mkfs.ext4, and never on the REUSE path (an
// already-correctly-labelled disk relies on udev's ordinary coldplug
// handling instead - see the comment there and in the systemd unit).
func TestReprobeDiskOnlyCalledAfterFormatting(t *testing.T) {
	src, err := os.ReadFile(formatVolumesScript)
	if err != nil {
		t.Fatalf("reading %s: %v", formatVolumesScript, err)
	}
	text := string(src)

	calls := regexp.MustCompile(`(?m)^\s*reprobe_disk `).FindAllString(text, -1)
	if len(calls) != 1 {
		t.Fatalf("expected exactly one reprobe_disk call site in the main loop, found %d: %v", len(calls), calls)
	}

	reuseBlock := regexp.MustCompile(`(?s)already ext4 labelled.*?\n[ \t]*fi\n`).FindString(text)
	if reuseBlock == "" {
		t.Fatal("could not locate the REUSE branch to check")
	}
	if strings.Contains(reuseBlock, "reprobe_disk") {
		t.Errorf("reprobe_disk must not be called on the reuse path:\n%s", reuseBlock)
	}

	formatBlock := regexp.MustCompile(`(?s)was blank, formatted ext4.*?\n[ \t]*else\n`).FindString(text)
	if formatBlock == "" {
		t.Fatal("could not locate the format-success branch to check")
	}
	if !strings.Contains(formatBlock, "reprobe_disk") {
		t.Errorf("reprobe_disk must be called right after a successful mkfs.ext4:\n%s", formatBlock)
	}
}
