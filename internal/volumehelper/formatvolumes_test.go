package volumehelper_test

import (
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

// extractExt4Label pulls just the ext4_label() function out of
// formatVolumesScript, so the test runs the real function the image
// ships (not a copy hand-maintained here) without also running the
// script's own main loop, which would read /etc/contemper/volumes and
// exit before a test could call the function at all.
func extractExt4Label(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(formatVolumesScript)
	if err != nil {
		t.Fatalf("reading %s: %v", formatVolumesScript, err)
	}
	re := regexp.MustCompile(`(?ms)^ext4_label\(\) \{.*?^\}$`)
	fn := re.Find(src)
	if fn == nil {
		t.Fatalf("ext4_label() function not found in %s", formatVolumesScript)
	}
	return string(fn)
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
