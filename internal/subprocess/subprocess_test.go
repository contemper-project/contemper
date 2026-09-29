package subprocess_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/contemper-project/contemper/internal/subprocess"
)

// trapScript runs until it receives SIGTERM, at which point it writes
// markerPath and exits, so the test can tell whether Command's Cancel
// reached it (rather than the process being SIGKILLed with no chance to
// react).
const trapScript = `#!/bin/sh
marker="$1"
trap 'echo terminated > "$marker"; exit 0' TERM
while true; do sleep 0.05; done
`

func writeTrapScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "trap.sh")
	if err := os.WriteFile(p, []byte(trapScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCommandSendsSIGTERMOnCancel(t *testing.T) {
	script := writeTrapScript(t)
	marker := filepath.Join(t.TempDir(), "marker")

	ctx, cancel := context.WithCancel(context.Background())
	cmd := subprocess.Command(ctx, "/bin/sh", script, marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}

	// Give the script a moment to install its trap before canceling.
	time.Sleep(100 * time.Millisecond)
	cancel()

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	select {
	case <-waitErr:
	case <-time.After(subprocess.GraceDelay + 5*time.Second):
		t.Fatal("subprocess did not exit after cancel")
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker file not written; process was likely killed instead of given SIGTERM: %v", err)
	}
	if string(data) != "terminated\n" {
		t.Errorf("marker content = %q, want %q", data, "terminated\n")
	}
}
