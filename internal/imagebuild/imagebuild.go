// Package imagebuild drives a container build engine - `docker buildx
// build` or `podman build` - as a subprocess, for `contemper build` to
// build an image before handing it to the docker-daemon: or
// containers-storage: source. The argv construction for each engine
// lives in one place (see Engine.Args) so that the command, its error
// messages and its hand-run alternatives all agree.
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

// Engine names a container build engine, or Auto to pick one.
type Engine string

const (
	// Auto picks Docker when it is usable, else Podman. See Resolve.
	Auto Engine = "auto"
	// Docker builds with `docker buildx build --load` and hands the
	// image over through the docker-daemon: source.
	Docker Engine = "docker"
	// Podman builds with `podman build` and hands the image over
	// through the containers-storage: source.
	Podman Engine = "podman"
)

// ParseEngine validates the value of an --engine flag.
func ParseEngine(s string) (Engine, error) {
	switch e := Engine(s); e {
	case Auto, Docker, Podman:
		return e, nil
	}
	return "", fmt.Errorf("--engine %q: must be auto, docker or podman", s)
}

// Options describes one build invocation.
type Options struct {
	// Context is the build context directory.
	Context string
	// File is the Containerfile/Dockerfile path; empty uses the
	// engine's own default (<Context>/Dockerfile). See DefaultFile.
	File string
	// Tag is the image tag to build and load as.
	Tag string
	// Platform is a single "os/arch" pair, e.g. "linux/amd64".
	Platform string
	// BuildArgs are "KEY=VALUE" strings, passed as repeated --build-arg.
	BuildArgs []string
}

// Args returns the argv the engine builds with, not including the
// leading program name: `buildx build --load ...` for docker, `build
// ...` for podman, which builds into its local image store without
// being asked to.
func (e Engine) Args(opts Options) []string {
	var args []string
	if e == Podman {
		args = []string{"build", "--platform", opts.Platform, "-t", opts.Tag}
	} else {
		args = []string{"buildx", "build", "--load", "--platform", opts.Platform, "-t", opts.Tag}
	}
	if opts.File != "" {
		args = append(args, "-f", opts.File)
	}
	for _, ba := range opts.BuildArgs {
		args = append(args, "--build-arg", ba)
	}
	return append(args, opts.Context)
}

// Command returns the full build invocation as a shell-quoted,
// copy-pasteable string, for messages shown when contemper cannot run
// it itself.
func (e Engine) Command(opts Options) string {
	return shellQuote(append([]string{string(e)}, e.Args(opts)...))
}

// BuildName is how errors from the engine's build are labelled:
// "docker buildx build" or "podman build".
func (e Engine) BuildName() string {
	if e == Podman {
		return "podman build"
	}
	return "docker buildx build"
}

// SourceRef returns the source reference the built image is converted
// through: docker-daemon:<tag> or containers-storage:<tag>.
func (e Engine) SourceRef(tag string) string {
	if e == Podman {
		return "containers-storage:" + tag
	}
	return "docker-daemon:" + tag
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

// Build runs the engine's build as a subprocess at enginePath, argv
// only (no shell). Both its stdout and stderr go to the current
// process's stderr (the engines write build progress there), so the
// caller's stdout carries only its own result; stdin is passed through
// for a context or file read from "-". Canceling ctx stops it.
func (e Engine) Build(ctx context.Context, enginePath string, opts Options) error {
	cmd := subprocess.Command(ctx, enginePath, e.Args(opts)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CheckAvailable verifies that the engine can build, returning its
// resolved path. Docker needs `docker` on PATH with a working buildx
// plugin, and not to be podman's docker emulation (which has no
// buildx); podman needs `podman` on PATH with a working `podman
// version`. On failure the error shows why and the exact command opts
// describes, shell-quoted and copy-pasteable, with a suggestion to
// build the image by hand and convert it - it names no engine
// invocation beyond the cheap check itself.
func (e Engine) CheckAvailable(ctx context.Context, opts Options) (string, error) {
	path, reason := e.check(ctx)
	if reason != "" {
		return "", fmt.Errorf("%s\n\n%s", reason, handRun("build the image by hand and run this instead", e, opts))
	}
	return path, nil
}

// Resolve picks the engine to build with. An explicit Docker or Podman
// is only checked for itself. Auto tries docker first, which keeps
// hosts that have both building as they always did, then podman; when
// neither is usable the error gives each engine's reason and both
// hand-run alternatives.
func Resolve(ctx context.Context, want Engine, opts Options) (Engine, string, error) {
	if want != Auto {
		path, err := want.CheckAvailable(ctx, opts)
		return want, path, err
	}
	dockerPath, dockerReason := Docker.check(ctx)
	if dockerReason == "" {
		return Docker, dockerPath, nil
	}
	podmanPath, podmanReason := Podman.check(ctx)
	if podmanReason == "" {
		return Podman, podmanPath, nil
	}
	return "", "", fmt.Errorf(
		"neither docker buildx nor podman is usable:\n\n  docker: %s\n  podman: %s\n\n%s",
		dockerReason, podmanReason,
		handRun("build the image by hand with either engine and run the matching pair instead", Docker, opts, Podman),
	)
}

// check reports why the engine is unusable, or "" and its path when it
// is usable.
func (e Engine) check(ctx context.Context) (path, reason string) {
	path, err := exec.LookPath(string(e))
	if err != nil {
		return "", string(e) + " is not installed, or not on PATH"
	}
	if e == Podman {
		if out, err := subprocess.Command(ctx, path, "version").CombinedOutput(); err != nil {
			return "", withOutput("podman is not working", out)
		}
		return path, ""
	}
	// podman's docker emulation answers `docker --version` as podman.
	if out, err := subprocess.Command(ctx, path, "--version").Output(); err == nil &&
		strings.HasPrefix(strings.TrimSpace(string(out)), "podman version") {
		return "", "docker is podman's docker emulation, which has no buildx"
	}
	if out, err := subprocess.Command(ctx, path, "buildx", "version").CombinedOutput(); err != nil {
		return "", withOutput("docker buildx is not available", out)
	}
	return path, ""
}

func withOutput(reason string, out []byte) string {
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		reason += ": " + trimmed
	}
	return reason
}

// handRun formats the hand-run alternative for each of engines: the
// build command and the contemper convert command for its image.
func handRun(intro string, first Engine, opts Options, more ...Engine) string {
	var b strings.Builder
	b.WriteString(intro + ":\n")
	for i, e := range append([]Engine{first}, more...) {
		if i > 0 {
			b.WriteString("\nor:\n")
		}
		fmt.Fprintf(&b, "\n  %s\n\n  contemper convert %s\n", e.Command(opts), e.SourceRef(opts.Tag))
	}
	return strings.TrimRight(b.String(), "\n")
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
