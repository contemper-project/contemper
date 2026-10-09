package source_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/source"
)

// fakePodmanScript is a plain /bin/sh script that stands in for the real
// `podman` binary in tests. It records every invocation's argv (one line,
// space-joined) to $FAKE_PODMAN_LOG and, for
// "save --format oci-archive -o <path> <ref>", copies
// $FAKE_PODMAN_ARCHIVE to <path>. Anything else fails.
const fakePodmanScript = `#!/bin/sh
echo "$@" >> "$FAKE_PODMAN_LOG"
if [ "$1" = "save" ] && [ "$2" = "--format" ] && [ "$3" = "oci-archive" ] && [ "$4" = "-o" ]; then
	cp "$FAKE_PODMAN_ARCHIVE" "$5"
	exit 0
fi
echo "fake podman: unsupported invocation: $*" >&2
exit 1
`

func installFakePodman(t *testing.T, archivePath string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(fakePodmanScript), 0o755); err != nil {
		t.Fatalf("writing fake podman script: %v", err)
	}
	logPath = filepath.Join(t.TempDir(), "podman.log")
	t.Setenv("FAKE_PODMAN_LOG", logPath)
	t.Setenv("FAKE_PODMAN_ARCHIVE", archivePath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func containersStorageTempDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "contemper-containers-storage-") {
			matches = append(matches, e.Name())
		}
	}
	return matches
}

func TestLoadContainersStorage(t *testing.T) {
	archiveFixture, wantDigest := buildDockerDaemonOCILayoutFixture(t)
	logPath := installFakePodman(t, archiveFixture)
	tmp := isolateTempDir(t)

	ref, err := source.ParseRef("containers-storage:my-app:dev")
	if err != nil {
		t.Fatal(err)
	}
	img, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if img.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", img.Digest, wantDigest)
	}
	if img.RepoBase != "my-app" || img.Tag != "dev" {
		t.Errorf("RepoBase/Tag = %s/%s, want my-app/dev", img.RepoBase, img.Tag)
	}
	if img.Reproducible {
		t.Errorf("containers-storage sources should be marked non-reproducible")
	}
	if img.Ref != ref {
		t.Errorf("Ref = %+v, want %+v", img.Ref, ref)
	}

	argv := strings.Fields(strings.TrimSpace(readLog(t, logPath)))
	if len(argv) != 6 || argv[0] != "save" || argv[1] != "--format" || argv[2] != "oci-archive" ||
		argv[3] != "-o" || argv[5] != "my-app:dev" ||
		filepath.Base(argv[4]) != "image.tar" || !strings.HasPrefix(argv[4], tmp+string(filepath.Separator)) {
		t.Errorf("podman argv = %q, want `save --format oci-archive -o %s/contemper-containers-storage-*/image.tar my-app:dev`", argv, tmp)
	}

	if len(containersStorageTempDirs(t)) == 0 {
		t.Fatalf("expected a contemper-containers-storage-* temp dir while the image is open")
	}
	if _, err := img.Image.Layers(); err != nil {
		t.Fatalf("reading layers after Load: %v", err)
	}
	img.Close()
	if got := containersStorageTempDirs(t); len(got) != 0 {
		t.Errorf("temp dir(s) %v still present after Close", got)
	}
}

func TestLoadContainersStorageNamesBundleFromRef(t *testing.T) {
	archiveFixture, _ := buildDockerDaemonOCILayoutFixture(t)
	installFakePodman(t, archiveFixture)
	isolateTempDir(t)

	for raw, want := range map[string]string{
		"containers-storage:my-app":                                   "my-app/latest",
		"containers-storage:localhost/my-app:dev":                     "my-app/dev",
		"containers-storage:localhost:5000/team/app:v2":               "app/v2",
		"containers-storage:my-app@sha256:" + strings.Repeat("a", 64): "my-app/latest",
	} {
		ref, err := source.ParseRef(raw)
		if err != nil {
			t.Fatal(err)
		}
		img, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH})
		if err != nil {
			t.Fatalf("Load(%s): %v", raw, err)
		}
		img.Close()
		if got := img.RepoBase + "/" + img.Tag; got != want {
			t.Errorf("Load(%s) RepoBase/Tag = %s, want %s", raw, got, want)
		}
	}
}

func TestLoadContainersStorageFailingSave(t *testing.T) {
	isolateTempDir(t)
	dir := t.TempDir()
	failing := "#!/bin/sh\necho fake podman save failure >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ref, _ := source.ParseRef("containers-storage:my-app:dev")
	_, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH})
	if err == nil || !strings.Contains(err.Error(), "podman save my-app:dev") {
		t.Fatalf("Load error = %v, want one naming `podman save my-app:dev`", err)
	}
	if got := containersStorageTempDirs(t); len(got) != 0 {
		t.Errorf("temp dir(s) %v left behind after a failed Load", got)
	}
}

func TestLoadContainersStorageMissingPodman(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	ref, _ := source.ParseRef("containers-storage:my-app:dev")
	_, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH})
	if err == nil {
		t.Fatalf("Load: expected an error when podman is not on PATH")
	}
	for _, want := range []string{"podman", "https://podman.io/docs/installation"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
