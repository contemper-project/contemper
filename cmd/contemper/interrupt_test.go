package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/subprocess"
)

// TestMain lets this test binary re-exec itself as plain "contemper": a
// child process started with CONTEMPER_TEST_REEXEC=1 runs main() (which
// installs contemper's own SIGINT/SIGTERM handling and calls os.Exit
// itself) instead of the test suite, so
// TestConvertSignalTerminatesAndCleansUp can send it a real
// signal and observe the process-level result - something calling
// runConvert directly, in-process, cannot exercise.
func TestMain(m *testing.M) {
	if os.Getenv("CONTEMPER_TEST_REEXEC") == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// slowToolScript stands in for a host tool (qemu-img, in these tests)
// that takes a while to run: it signals that it has started by creating
// $SLOW_TOOL_MARKER, then waits for either an interrupt (in which case
// it records having received SIGTERM in $SLOW_TOOL_TERM_MARKER, mirroring
// what a real tool given a chance to exit cleanly would do) or the test
// timing out and killing it.
//
// The trap is installed before the start marker is created, so a signal
// sent as soon as the marker shows up is always caught.
const slowToolScript = `#!/bin/sh
trap 'touch "$SLOW_TOOL_TERM_MARKER"; exit 0' TERM
touch "$SLOW_TOOL_MARKER"
while true; do sleep 0.05; done
`

// fastQemuImgScript stands in for a qemu-img that finishes at once: for
// "qemu-img convert -O qcow2 SRC DST" it writes a small placeholder to
// DST, enough for a conversion to finish and commit a bundle.
const fastQemuImgScript = `#!/bin/sh
printf 'placeholder qcow2\n' > "$5"
`

// installFakeSlowTool puts slowToolScript on PATH as name (e.g.
// "qemu-img") in its own directory, and returns that directory plus the
// marker paths the script will create.
func installFakeSlowTool(t *testing.T, name string) (dir, marker, termMarker string) {
	t.Helper()
	markerDir := t.TempDir()
	return installFakeTool(t, name, slowToolScript), filepath.Join(markerDir, "started"), filepath.Join(markerDir, "terminated")
}

// waitForFile polls for path to exist, failing the test if it doesn't
// show up within timeout.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// requireExt4HostTools skips the test unless the real mkfs.ext4/debugfs/
// e2fsck are available - these tests run a real conversion up to the
// qcow2 step (where a fake, deliberately slow qemu-img takes over), the
// same way internal/disk's own PopulateExt4 tests do.
func requireExt4HostTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"mkfs.ext4", "debugfs", "e2fsck"} {
		if hostenv.Find(name) == "" {
			t.Skipf("%s not found; skipping interrupt test", name)
		}
	}
}

// tempDirEntries lists the base names of dir's contents, for asserting a
// temp/output directory was left empty.
func tempDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// installFakeTool puts script on PATH as name, in its own directory,
// and returns that directory.
func installFakeTool(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// snapshotDir maps every regular file under dir (by path relative to
// dir) to its content, for asserting a directory was left untouched.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	return files
}

// TestConvertContextCancelCleansUpTempFiles runs a real conversion
// in-process (mkfs.ext4/debugfs/e2fsck for real, a fake slow qemu-img
// standing in for the qcow2 step) and cancels runConvert's context once
// the slow step has started, mirroring what contemper's own SIGINT/
// SIGTERM handling does to it. It checks that the fake qemu-img was
// asked to stop (rather than left running), that runConvert returns an
// error, and that TMPDIR and --out are left exactly as they started:
// TMPDIR empty, with no partial rootfs/assemble/payload temp
// directories, and --out holding only the previous bundle (from an
// earlier, successful conversion), unchanged, with no staging directory
// next to it.
func TestConvertContextCancelCleansUpTempFiles(t *testing.T) {
	requireExt4HostTools(t)

	archivePath := buildFixtureArchive(t)
	outDir := t.TempDir()
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	opts := convertOptions{
		sourceRef:    "oci-archive:" + archivePath,
		target:       "qemu",
		outDir:       outDir,
		progressMode: "auto",
		quiet:        true,
	}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	// A first, successful conversion leaves a previous bundle in --out,
	// which the interrupted one below must not touch.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", installFakeTool(t, "qemu-img", fastQemuImgScript)+string(os.PathListSeparator)+origPath)
	if err := runConvert(t.Context(), cmd, opts); err != nil {
		t.Fatalf("first conversion: %v", err)
	}
	previous := tempDirEntries(t, outDir)
	if len(previous) != 1 {
		t.Fatalf("--out after the first conversion = %v, want exactly one bundle", previous)
	}
	previousBundle := snapshotDir(t, outDir)
	if names := tempDirEntries(t, tmpDir); len(names) != 0 {
		t.Fatalf("TMPDIR left non-empty after a successful conversion: %v", names)
	}

	binDir, marker, termMarker := installFakeSlowTool(t, "qemu-img")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("SLOW_TOOL_MARKER", marker)
	t.Setenv("SLOW_TOOL_TERM_MARKER", termMarker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runConvert(ctx, cmd, opts) }()

	// Wait for the conversion to reach the (fake, slow) qcow2 step before
	// canceling, so real work - reading the archive, building the
	// rootfs, populating the ext4 image - has actually happened and left
	// its own temp directories behind for cleanup to remove.
	waitForFile(t, marker, 30*time.Second)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("runConvert returned no error after its context was canceled")
		}
	case <-time.After(subprocess.GraceDelay + 10*time.Second):
		t.Fatal("runConvert did not return after its context was canceled")
	}

	// The fake qemu-img was sent SIGTERM (not just abandoned running).
	waitForFile(t, termMarker, subprocess.GraceDelay+5*time.Second)

	if names := tempDirEntries(t, tmpDir); len(names) != 0 {
		t.Errorf("TMPDIR left non-empty after the context was canceled: %v", names)
	}
	if names := tempDirEntries(t, outDir); !slices.Equal(names, previous) {
		t.Errorf("--out after the context was canceled = %v, want only the previous bundle %v", names, previous)
	}
	if got := snapshotDir(t, outDir); !maps.Equal(got, previousBundle) {
		t.Error("the previous bundle in --out changed after the context was canceled")
	}
}

// TestConvertSignalTerminatesAndCleansUp re-execs this test binary as
// plain "contemper convert" (see TestMain) and sends it a real SIGINT or
// SIGTERM once a fake, deliberately slow qemu-img has started, checking
// the process-level contract: contemper prints one short "interrupted"
// line (not a wrapped error), stops the subprocess it was running,
// leaves no temp files behind, and ends terminated by that same signal,
// which a shell reports as exit code 130 or 143.
func TestConvertSignalTerminatesAndCleansUp(t *testing.T) {
	requireExt4HostTools(t)
	archivePath := buildFixtureArchive(t)
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolving the test binary's own path: %v", err)
	}

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			if signal.Ignored(sig) {
				// The re-exec'd contemper would inherit the ignore and,
				// by design, keep it.
				t.Skipf("%v is ignored in this test process", sig)
			}
			outDir := t.TempDir()
			tmpDir := t.TempDir()
			binDir, marker, termMarker := installFakeSlowTool(t, "qemu-img")

			cmd := exec.CommandContext(t.Context(), exePath, "convert", "--target", "qemu", "--out", outDir, "--quiet", "oci-archive:"+archivePath)
			cmd.Env = append(os.Environ(),
				"CONTEMPER_TEST_REEXEC=1",
				"TMPDIR="+tmpDir,
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SLOW_TOOL_MARKER="+marker,
				"SLOW_TOOL_TERM_MARKER="+termMarker,
			)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			if err := cmd.Start(); err != nil {
				t.Fatalf("starting the re-exec'd contemper: %v", err)
			}

			waitForFile(t, marker, 30*time.Second)
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatalf("sending %v: %v", sig, err)
			}

			waitErrCh := make(chan error, 1)
			go func() { waitErrCh <- cmd.Wait() }()
			var waitErr error
			select {
			case waitErr = <-waitErrCh:
			case <-time.After(subprocess.GraceDelay + 15*time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("the re-exec'd contemper did not exit after %v", sig)
			}

			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Fatalf("contemper exited with %v (want terminated by %v); stderr:\n%s", waitErr, sig, stderr.String())
			}
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != sig {
				t.Errorf("contemper ended with %v, want terminated by %v; stderr:\n%s", exitErr, sig, stderr.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != "contemper: interrupted" {
				t.Errorf("stderr = %q, want exactly %q (not a wrapped error)", got, "contemper: interrupted")
			}

			// The fake qemu-img was sent SIGTERM by contemper's own
			// cleanup, not left running as an orphan.
			waitForFile(t, termMarker, subprocess.GraceDelay+5*time.Second)

			if names := tempDirEntries(t, tmpDir); len(names) != 0 {
				t.Errorf("TMPDIR left non-empty after %v: %v", sig, names)
			}
			if names := tempDirEntries(t, outDir); len(names) != 0 {
				t.Errorf("--out left non-empty after %v: %v", sig, names)
			}
		})
	}
}
