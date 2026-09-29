// Package subprocess builds *exec.Cmd values for the host tools contemper
// shells out to (mkfs.ext4, debugfs, e2fsck, qemu-img, qemu-system-*,
// docker), with consistent behavior when their context is canceled - in
// particular, when contemper itself is asked to stop by SIGINT or
// SIGTERM (see cmd/contemper's signal handling). exec.CommandContext's
// own default on cancellation is to kill the process outright
// (SIGKILL); Command instead sends SIGTERM first, giving the tool a
// chance to leave the file it's writing in a consistent state, and only
// kills it if it hasn't exited within GraceDelay.
package subprocess

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// GraceDelay is how long a subprocess started by Command is given to
// exit after its context is canceled and it receives SIGTERM, before
// Cmd.Wait kills it outright with SIGKILL. As exec.Cmd.WaitDelay, it
// also bounds how long Wait keeps copying output from a stdout/stderr
// that isn't an *os.File once the process has exited, should something
// the tool started still hold that pipe open.
const GraceDelay = 5 * time.Second

// Command returns an *exec.Cmd for name/args, argv only (no shell),
// bound to ctx: once ctx is done, the subprocess is sent SIGTERM rather
// than killed immediately, and forcibly killed if it is still running
// GraceDelay later. Every contemper subprocess should be built through
// this, so a signal that reaches contemper also reaches (and gives a
// clean-exit chance to) whatever host tool it is currently running.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: name/args are the caller's own fixed host-tool invocation, never a shell
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = GraceDelay
	return cmd
}
