// Package imagebuild drives `docker buildx build` as a subprocess, for
// `contemper build` to build an image before handing it to the
// docker-daemon: source. The argv construction lives in one place (see
// Args) so a second build engine can be added later without reshaping
// the command.
package imagebuild

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/contemper-project/contemper/internal/subprocess"
)

// Options describes one `docker buildx build --load` invocation.
type Options struct {
	// Context is the build context directory.
	Context string
	// File is the Containerfile/Dockerfile path; empty uses buildx's own
	// default (<Context>/Dockerfile). See DefaultFile.
	File string
	// Tag is the image tag to build and load as.
	Tag string
	// Platform is a single "os/arch" pair, e.g. "linux/amd64".
	Platform string
	// BuildArgs are "KEY=VALUE" strings, passed as repeated --build-arg.
	BuildArgs []string
}

// Args returns the argv `docker buildx build --load` runs with, not
// including the leading "docker".
func Args(opts Options) []string {
	args := []string{"buildx", "build", "--load", "--platform", opts.Platform, "-t", opts.Tag}
	if opts.File != "" {
		args = append(args, "-f", opts.File)
	}
	for _, ba := range opts.BuildArgs {
		args = append(args, "--build-arg", ba)
	}
	return append(args, opts.Context)
}

// Command returns the full "docker buildx build ..." invocation as a
// shell-quoted, copy-pasteable string, for messages shown when contemper
// cannot run it itself.
func Command(opts Options) string {
	return shellQuote(append([]string{"docker"}, Args(opts)...))
}

// DefaultFile returns the build file to pass as -f for contextDir when
// the user named none: "" (buildx's own default, <contextDir>/Dockerfile)
// when contextDir has a Dockerfile, <contextDir>/Containerfile when it
// has only a Containerfile, which docker does not look for by itself,
// and "" when it has neither or isn't a local directory at all (a Git
// URL, say), leaving the error or the remote lookup to buildx.
func DefaultFile(contextDir string) string {
	if isFile(filepath.Join(contextDir, "Dockerfile")) {
		return ""
	}
	if containerfile := filepath.Join(contextDir, "Containerfile"); isFile(containerfile) {
		return containerfile
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Build runs `docker buildx build --load` as a subprocess at dockerPath,
// argv only (no shell). Both its stdout and stderr go to the current
// process's stderr (buildx writes build progress there), so the
// caller's stdout carries only its own result; stdin is passed through
// for a context or file read from "-". Canceling ctx stops it.
func Build(ctx context.Context, dockerPath string, opts Options) error {
	args := Args(opts)
	cmd := subprocess.Command(ctx, dockerPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CheckAvailable verifies that docker is on PATH and its buildx plugin
// works, returning the resolved docker path. On failure it returns an
// error that shows the exact command opts describes, shell-quoted and
// copy-pasteable, along with a suggestion to build the image by hand and
// convert it with `contemper convert docker-daemon:<tag>` - it names no
// docker/buildx invocation beyond `docker buildx version`, the check
// itself.
func CheckAvailable(ctx context.Context, opts Options) (string, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return "", unavailableError(opts, "docker is not installed, or not on PATH")
	}
	cmd := subprocess.Command(ctx, dockerPath, "buildx", "version")
	if out, err := cmd.CombinedOutput(); err != nil {
		reason := "docker buildx is not available"
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			reason += ": " + trimmed
		}
		return "", unavailableError(opts, reason)
	}
	return dockerPath, nil
}

func unavailableError(opts Options, reason string) error {
	return fmt.Errorf(
		"%s\n\nbuild the image by hand and run this instead:\n\n  %s\n\n  contemper convert docker-daemon:%s",
		reason, Command(opts), opts.Tag,
	)
}

// shellQuote joins args into a POSIX-shell-safe, copy-pasteable command
// line: any argument that is empty or contains a character a shell would
// treat specially is single-quoted (with embedded single quotes escaped
// the usual '\” way); everything else is left bare for readability.
func shellQuote(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuoteArg(a)
	}
	return strings.Join(quoted, " ")
}

const shellSafeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%_-+=:,./"

func shellQuoteArg(a string) string {
	if a != "" && strings.Trim(a, shellSafeChars) == "" {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}
