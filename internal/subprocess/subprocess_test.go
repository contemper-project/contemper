package subprocess_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/contemper-project/contemper/internal/subprocess"
)

// trapScript creates its first argument once its SIGTERM trap is in
// place, then runs until it receives SIGTERM, at which point it writes
// its second argument and exits, so the test can tell whether Command's
// Cancel reached it (rather than the process being SIGKILLed with no
// chance to react).
const trapScript = `#!/bin/sh
marker="$2"
trap 'echo terminated > "$marker"; exit 0' TERM
touch "$1"
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
	markerDir := t.TempDir()
	started := filepath.Join(markerDir, "started")
	marker := filepath.Join(markerDir, "marker")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := subprocess.Command(ctx, "/bin/sh", script, started, marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}

	// Cancel only once the script's trap is in place.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the script did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
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
