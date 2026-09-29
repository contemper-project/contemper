package qemu

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeQemuScript stands in for qemu-system-*: it creates $FAKE_QEMU_STARTED
// once its trap is in place, then runs until SIGINT or SIGTERM and exits
// 0, the way qemu shuts down cleanly when a user presses Ctrl-C.
const fakeQemuScript = `#!/bin/sh
trap 'exit 0' INT TERM
touch "$FAKE_QEMU_STARTED"
while true; do sleep 0.05; done
`

// TestDeployCanceledReportsInterruptAndCleansUp checks that canceling
// Deploy's context stops qemu, removes Deploy's own temp files (the
// generated serial log and qemu work dir), and returns the context's
// error even though qemu itself exited cleanly.
func TestDeployCanceledReportsInterruptAndCleansUp(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "qemu-system-fake"), []byte(fakeQemuScript), 0o755); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(t.TempDir(), "started")
	tmpDir := t.TempDir()
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_QEMU_STARTED", started)
	t.Setenv("TMPDIR", tmpDir)

	archTable["fake"] = archInfo{
		systemBinary:     "qemu-system-fake",
		machine:          "virt",
		pflashCandidates: []firmware{{code: writeFakeFile(t, "code.fd"), vars: writeFakeFile(t, "vars.fd")}},
	}
	t.Cleanup(func() { delete(archTable, "fake") })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Deploy(ctx, Options{Arch: "fake", DiskPath: filepath.Join(t.TempDir(), "disk.qcow2"), DiskFormat: "qcow2"})
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fake qemu did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Deploy returned %v, want context.Canceled", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Deploy did not return after its context was canceled")
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("TMPDIR left non-empty after Deploy was canceled: %s", e.Name())
	}
}
