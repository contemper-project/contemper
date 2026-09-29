package main

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/contemper-project/contemper/internal/bundle"
	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/imgtest"
)

// fakeBuildDockerScript stands in for the real `docker` binary: it
// records every invocation's argv to $FAKE_DOCKER_LOG and handles the
// three subcommands `build` runs - "buildx version" (health check),
// "buildx build ..." (pretends to build, does nothing) and "save -o
// <path> <ref>" (copies the prebuilt fixture archive named by
// $FAKE_DOCKER_ARCHIVE to <path>, standing in for a real docker save of
// whatever "buildx build" just loaded).
const fakeBuildDockerScript = `#!/bin/sh
echo "$@" >> "$FAKE_DOCKER_LOG"
case "$1 $2" in
	"buildx version")
		exit 0
		;;
	"buildx build")
		# Real buildx can write to stdout too; none of it may reach
		# contemper's own stdout.
		echo "buildx-stdout-noise"
		exit 0
		;;
esac
if [ "$1" = "save" ]; then
	shift
	out=""
	while [ $# -gt 0 ]; do
		case "$1" in
			-o) out="$2"; shift 2 ;;
			*) shift ;;
		esac
	done
	if [ -z "$out" ]; then
		echo "fake docker save: no -o given" >&2
		exit 1
	fi
	cp "$FAKE_DOCKER_ARCHIVE" "$out"
	exit 0
fi
echo "fake docker: unsupported invocation: $*" >&2
exit 1
`

// installFakeBuildDocker puts fakeBuildDockerScript on PATH ahead of the
// real PATH as "docker" (so real host tools like mkfs.ext4/qemu-img,
// needed by the conversion step, stay reachable), and has it serve
// archivePath for `docker save`.
func installFakeBuildDocker(t *testing.T, archivePath string) (logPath string) {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeBuildDockerScript), 0o755); err != nil {
		t.Fatalf("writing fake docker script: %v", err)
	}

	logPath = filepath.Join(t.TempDir(), "docker.log")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_ARCHIVE", archivePath)
	return logPath
}

// buildFixtureArchive writes a synthetic contemper-ready image (a
// self-contained kernel/initrd/init, needing no support image, like
// internal/target's assembler tests use) as an OCI layout tarball - the
// shape a containerd-backed `docker save` produces, and the shape the
// fake docker script above hands back for every `docker save`.
func buildFixtureArchive(t *testing.T) string {
	t.Helper()

	plat := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	files := []imgtest.File{
		{Path: "boot/", Typeflag: tar.TypeDir},
		{Path: "boot/contemper/", Typeflag: tar.TypeDir},
		{Path: "boot/contemper/vmlinuz", Data: append([]byte("MZ"), make([]byte, 128)...)},
		{Path: "boot/contemper/initrd", Data: []byte("fake-initrd-content")},
		{Path: "boot/contemper/cmdline", Data: []byte("rw console=ttyS0\n")},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/os-release", Data: []byte("NAME=Test\n")},
	}
	img, err := imgtest.Image(plat, map[string]string{"io.contemper.ready": "true"}, files)
	if err != nil {
		t.Fatal(err)
	}

	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add:        img,
		Descriptor: v1.Descriptor{Platform: &plat},
	})

	layoutDir := t.TempDir()
	if _, err := layout.Write(layoutDir, idx); err != nil {
		t.Fatalf("writing layout: %v", err)
	}

	archivePath := filepath.Join(t.TempDir(), "docker-save.tar")
	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	tw := tar.NewWriter(out)
	defer func() { _ = tw.Close() }()
	err = filepath.Walk(layoutDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(layoutDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		t.Fatalf("tarring layout: %v", err)
	}
	return archivePath
}

// TestBuildCommandProducesBundle runs `contemper build` end to end
// against a fake `docker` (buildx version/build are no-ops; save hands
// back a prebuilt fixture archive) but real host tools (mkfs.ext4,
// qemu-img, ...), and checks it produces a real bundle - the same shape
// `convert`'s own tests would check, skipped here (like those) when the
// host tools a real conversion needs aren't installed.
func TestBuildCommandProducesBundle(t *testing.T) {
	for _, name := range []string{"mkfs.ext4", "debugfs", "e2fsck", "qemu-img"} {
		if hostenv.Find(name) == "" {
			t.Skipf("%s not found; skipping build command test", name)
		}
	}

	archivePath := buildFixtureArchive(t)
	logPath := installFakeBuildDocker(t, archivePath)

	// Only a Containerfile, so build has to name it with -f itself.
	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "Containerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	cmd := newBuildCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--target", "qemu", "--tag", "my-app:dev", "--out", outDir, ctxDir})

	processStdout := captureProcessStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("build: %v", err)
		}
	})
	if processStdout != "" {
		t.Errorf("process stdout = %q, want nothing beyond the command's own output", processStdout)
	}

	wantBundle := filepath.Join(outDir, "my-app-dev."+machineArch(runtime.GOARCH))
	if got := stdout.String(); got != wantBundle+"\n" {
		t.Fatalf("build stdout = %q, want the bundle path %q", got, wantBundle+"\n")
	}
	bundleDir := wantBundle
	manifest, err := bundle.Read(bundleDir)
	if err != nil {
		t.Fatalf("reading bundle manifest: %v", err)
	}
	if manifest.Target != "qemu-qcow2" {
		t.Errorf("manifest.Target = %q, want qemu-qcow2", manifest.Target)
	}
	if manifest.Reproducible {
		t.Errorf("a docker-daemon-sourced bundle should not be marked reproducible")
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading docker invocation log: %v", err)
	}
	logStr := string(log)
	if !strings.Contains(logStr, "buildx version") {
		t.Errorf("docker log = %q, want a buildx version check", logStr)
	}
	wantBuild := "buildx build --load --platform linux/" + runtime.GOARCH + " -t my-app:dev -f " + filepath.Join(ctxDir, "Containerfile") + " " + ctxDir
	if !strings.Contains(logStr, wantBuild+"\n") {
		t.Errorf("docker log = %q, want %q", logStr, wantBuild)
	}
	if !strings.Contains(logStr, "save -o ") || !strings.Contains(logStr, " my-app:dev\n") {
		t.Errorf("docker log = %q, want a save -o <path> my-app:dev invocation", logStr)
	}

	// The docker save archive is gone once build returns.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "contemper-docker-daemon-") {
			t.Errorf("temp dir %s left behind after build", e.Name())
		}
	}
}

// TestBuildCommandImageOnly checks that --image-only stops after the
// build, prints only the image reference, doesn't need --target, and
// never runs docker save.
func TestBuildCommandImageOnly(t *testing.T) {
	logPath := installFakeBuildDocker(t, filepath.Join(t.TempDir(), "unused.tar"))

	cmd := newBuildCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--image-only", "--tag", "my-app:dev", t.TempDir()})

	processStdout := captureProcessStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("build --image-only: %v", err)
		}
	})
	if processStdout != "" {
		t.Errorf("process stdout = %q, want nothing beyond the command's own output", processStdout)
	}
	if got := stdout.String(); got != "my-app:dev\n" {
		t.Errorf("build --image-only stdout = %q, want %q", got, "my-app:dev\n")
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "save") {
		t.Errorf("docker log = %q, want no save with --image-only", log)
	}
}

// TestBuildCommandChecksFlagsBeforeBuilding checks that a missing or bad
// convert flag is reported before any docker command runs.
func TestBuildCommandChecksFlagsBeforeBuilding(t *testing.T) {
	for _, args := range [][]string{
		{"--tag", "my-app:dev"},
		{"--target", "no-such-target", "--tag", "my-app:dev"},
		{"--target", "qemu", "--root-size", "lots", "--tag", "my-app:dev"},
		{"--target", "qemu", "--tag", "Not A Tag"},
	} {
		logPath := installFakeBuildDocker(t, filepath.Join(t.TempDir(), "unused.tar"))
		cmd := newBuildCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append(args, t.TempDir()))
		if err := cmd.Execute(); err == nil {
			t.Errorf("build %v: expected an error", args)
		}
		if log, err := os.ReadFile(logPath); err == nil && len(log) > 0 {
			t.Errorf("build %v ran docker before failing: %q", args, log)
		}
	}
}

// captureProcessStdout runs fn with os.Stdout pointed at a temporary
// file and returns what was written there, for checking that nothing a
// subprocess prints leaks into the process's own stdout.
func captureProcessStdout(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig }()
	fn()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestBuildCommandMissingDocker checks the missing-docker path: no host
// tools are needed for it, so it always runs.
func TestBuildCommandMissingDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	ctxDir := t.TempDir()
	cmd := newBuildCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--target", "qemu", "--tag", "my-app:dev", ctxDir})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("build: expected an error when docker is not on PATH")
	}
	if !strings.Contains(err.Error(), "docker buildx build") || !strings.Contains(err.Error(), "contemper convert docker-daemon:my-app:dev") {
		t.Errorf("error %q does not show the copy-pasteable command and the convert suggestion", err.Error())
	}
}
