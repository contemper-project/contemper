// Command contemper converts a contemper-ready OCI image into a bootable
// VM disk bundle, and can deploy that bundle locally for testing.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/buildinfo"
	"github.com/contemper-project/contemper/internal/progress"
)

func main() {
	ctx, stop := notifyInterrupt(context.Background())
	err := newRootCmd().ExecuteContext(ctx)
	sig := stop()

	code, msg := exitStatus(err, sig)
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if code == 0 {
		return
	}
	if sig != nil && err != nil {
		reraise(sig)
	}
	os.Exit(code)
}

// exitStatus decides how contemper exits, given the error the command
// returned and the signal (if any) that interrupted it: the exit code,
// and the one line to print on stderr (empty for none).
//
// A command that returned nil finished its work, so it exits 0 even if a
// signal arrived meanwhile: the signal came too late to interrupt
// anything, and a caller would otherwise see a finished result (a bundle
// path on stdout) next to a failure exit code. A command that returned
// an error after a signal was interrupted by it: that error is very
// likely just "context canceled" (or a subprocess reporting it was
// stopped), wrapped a few layers deep, so a plain "interrupted" is
// printed instead, with the shell-convention exit code for the signal.
func exitStatus(err error, sig os.Signal) (code int, msg string) {
	switch {
	case err == nil:
		return 0, ""
	case sig != nil:
		return signalExitCode(sig), "contemper: interrupted"
	default:
		return 1, "contemper: " + progress.Sanitize(err.Error())
	}
}

// notifyInterrupt returns a context canceled on the process's first
// SIGINT or SIGTERM, so every subprocess and long-running step threaded
// through it (see cmd.Context(), passed down to source loads, the
// rootfs merge, disk assembly, and deploy) can stop and clean up its own
// temp files instead of leaving them behind.
//
// Once that first signal has arrived, SIGINT and SIGTERM get their
// default behavior back, so a second one kills the process right away
// (the usual double-Ctrl-C), without waiting for cleanup that isn't
// happening fast enough.
//
// A signal contemper was started with ignored stays ignored (see
// interruptSignals).
//
// The caller must call stop once the command has returned. It releases
// the signal handling and reports which signal canceled ctx, or nil if
// none did. signal.NotifyContext from the standard library works the
// same way, but doesn't say which signal it saw, which the exit code
// needs.
func notifyInterrupt(parent context.Context) (ctx context.Context, stop func() os.Signal) {
	ctx, cancel := context.WithCancel(parent)
	sigCh := make(chan os.Signal, 1)
	if sigs := interruptSignals(signal.Ignored); len(sigs) > 0 {
		signal.Notify(sigCh, sigs...)
	}

	var got os.Signal
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case got = <-sigCh:
			signal.Stop(sigCh)
			cancel()
		case <-stopped:
		}
	}()

	var once sync.Once
	return ctx, func() os.Signal {
		once.Do(func() {
			close(stopped)
			<-done
			signal.Stop(sigCh)
			cancel()
		})
		return got
	}
}

// interruptSignals returns the signals notifyInterrupt handles: SIGINT
// and SIGTERM, minus any that ignored reports the process was started
// with ignored. A shell starts a background job in a script (`contemper
// convert ... &`) with SIGINT ignored, so that a Ctrl-C meant for the
// foreground job leaves it running; signal.Notify would undo that, so
// such a signal is left alone.
func interruptSignals(ignored func(os.Signal) bool) []os.Signal {
	var sigs []os.Signal
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !ignored(sig) {
			sigs = append(sigs, sig)
		}
	}
	return sigs
}

// signalExitCode returns the shell-convention exit code for sig: 128 +
// the signal number (130 for SIGINT, 143 for SIGTERM).
func signalExitCode(sig os.Signal) int {
	if sig == syscall.SIGTERM {
		return 143
	}
	return 130 // os.Interrupt (SIGINT)
}

// reraise ends the process by sending sig to itself with its default
// action restored, so the parent sees contemper terminated by that
// signal (a shell reports $? as 128 + the signal number, the same code
// exitStatus picks) rather than a plain exit. A shell running contemper
// from a script or loop relies on this to tell that the user interrupted
// the whole thing, not just contemper, and stop too. Should the signal
// not end the process after all, reraise returns and the caller exits
// normally.
func reraise(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	signal.Reset(s)
	if err := syscall.Kill(syscall.Getpid(), s); err != nil {
		return
	}
	// The signal is normally delivered before Kill returns; give it a
	// moment anyway before falling back to a plain exit.
	time.Sleep(500 * time.Millisecond)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "contemper",
		Short:         "Convert OCI images into bootable VM disk bundles",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newBuildCmd())
	root.AddCommand(newConvertCmd())
	root.AddCommand(newDeployCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newGenDocsCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the contemper version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.Get().String())
			return nil
		},
	}
}
