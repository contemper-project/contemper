package source_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/source"
)

// fakeDockerScript is a plain /bin/sh script (works on macOS too) that
// stands in for the real `docker` binary in tests. It records every
// invocation's argv (one line, space-joined) to logPath, and:
//   - for "buildx version", exits 0 with no output;
//   - for "save -o <path> <ref>", copies archiveFixture to <path>;
//   - for anything else, exits 1.
const fakeDockerScript = `#!/bin/sh
echo "$@" >> "$FAKE_DOCKER_LOG"
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

// installFakeDocker puts fakeDockerScript on PATH (ahead of the real
// PATH) as "docker", records its argv to a fresh log file (returned,
// along with a reader helper) and has it serve archivePath for any
// `docker save -o <path> <ref>` invocation.
func installFakeDocker(t *testing.T, archivePath string) (logPath string) {
	t.Helper()

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatalf("writing fake docker script: %v", err)
	}

	logPath = filepath.Join(t.TempDir(), "docker.log")
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("FAKE_DOCKER_ARCHIVE", archivePath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readLog(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading %s: %v", logPath, err)
	}
	return string(data)
}

// buildDockerDaemonOCILayoutFixture builds a synthetic contemper-ready
// image for the host platform and writes it as an OCI layout tarball -
// the form Docker's containerd-backed image store writes for `docker
// save` (index.json at the archive root).
func buildDockerDaemonOCILayoutFixture(t *testing.T) (archivePath string, digest v1.Hash) {
	t.Helper()

	plat := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	img, err := imgtest.Image(plat, map[string]string{"io.contemper.ready": "true"},
		[]imgtest.File{{Path: "marker", Data: []byte("oci-layout")}})
	if err != nil {
		t.Fatal(err)
	}
	digest, err = img.Digest()
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

	// tarLayout (defined in source_test.go) tars a layout directory into
	// an archive file - exactly the shape a Docker 25+ `docker save`
	// produces for a containerd-backed image store.
	archivePath = tarLayout(t, layoutDir, "docker-save.tar")
	return archivePath, digest
}

// buildDockerDaemonDockerArchiveFixture builds a synthetic contemper-ready
// image for the host platform and writes it as a legacy docker-archive
// tarball (manifest.json, no index.json) - the form pre-containerd-store
// Docker writes for `docker save`.
func buildDockerDaemonDockerArchiveFixture(t *testing.T, repoTag string) (archivePath string, digest v1.Hash) {
	t.Helper()

	plat := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	img, err := imgtest.Image(plat, map[string]string{"io.contemper.ready": "true"},
		[]imgtest.File{{Path: "marker", Data: []byte("docker-archive")}})
	if err != nil {
		t.Fatal(err)
	}
	digest, err = img.Digest()
	if err != nil {
		t.Fatal(err)
	}

	tag, err := name.NewTag(repoTag)
	if err != nil {
		t.Fatalf("name.NewTag(%q): %v", repoTag, err)
	}

	archivePath = filepath.Join(t.TempDir(), "docker-save.tar")
	if err := tarball.WriteToFile(archivePath, tag, img); err != nil {
		t.Fatalf("writing docker-archive: %v", err)
	}
	return archivePath, digest
}

func TestLoadDockerDaemonOCILayout(t *testing.T) {
	archiveFixture, wantDigest := buildDockerDaemonOCILayoutFixture(t)
	logPath := installFakeDocker(t, archiveFixture)
	tmp := isolateTempDir(t)

	ref, err := source.ParseRef("docker-daemon:my-app:dev")
	if err != nil {
		t.Fatal(err)
	}
	platform := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}

	img, err := source.Load(t.Context(), ref, platform)
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
		t.Errorf("docker-daemon sources should be marked non-reproducible")
	}

	log := strings.TrimSpace(readLog(t, logPath))
	argv := strings.Fields(log)
	if len(argv) != 4 || argv[0] != "save" || argv[1] != "-o" || argv[3] != "my-app:dev" ||
		filepath.Base(argv[2]) != "image.tar" || !strings.HasPrefix(argv[2], tmp+string(filepath.Separator)) {
		t.Errorf("docker log = %q, want exactly `save -o %s/contemper-docker-daemon-*/image.tar my-app:dev`", log, tmp)
	}

	// The temp archive/layout dir must still exist while layers are
	// unread (convert streams them late) ...
	tmpDirs := tempDirsUnder(t)
	if len(tmpDirs) == 0 {
		t.Fatalf("expected a contemper-docker-daemon-* temp dir while the image is open")
	}
	if _, err := img.Image.Layers(); err != nil {
		t.Fatalf("reading layers after Load: %v", err)
	}

	// ... and gone once the caller is done with it.
	img.Close()
	if got := tempDirsUnder(t); len(got) != 0 {
		t.Errorf("temp dir(s) %v still present after Close", got)
	}
}

func TestLoadDockerDaemonDockerArchive(t *testing.T) {
	archiveFixture, wantDigest := buildDockerDaemonDockerArchiveFixture(t, "legacy-app:dev")
	installFakeDocker(t, archiveFixture)

	ref, err := source.ParseRef("docker-daemon:legacy-app:dev")
	if err != nil {
		t.Fatal(err)
	}
	platform := v1.Platform{OS: "linux", Architecture: runtime.GOARCH}

	img, err := source.Load(t.Context(), ref, platform)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer img.Close()

	if img.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", img.Digest, wantDigest)
	}
	if img.RepoBase != "legacy-app" || img.Tag != "dev" {
		t.Errorf("RepoBase/Tag = %s/%s, want legacy-app/dev", img.RepoBase, img.Tag)
	}
	if img.Reproducible {
		t.Errorf("docker-daemon sources should be marked non-reproducible")
	}
}

func TestLoadDockerDaemonCleansUpOnError(t *testing.T) {
	isolateTempDir(t)
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "docker")
	failing := "#!/bin/sh\necho fake docker save failure >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ref, err := source.ParseRef("docker-daemon:my-app:dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH}); err == nil {
		t.Fatalf("Load: expected an error when docker save fails")
	}
	if got := tempDirsUnder(t); len(got) != 0 {
		t.Errorf("temp dir(s) %v left behind after a failed Load", got)
	}
}

// TestLoadDockerDaemonCleansUpOnUnreadableArchive covers the failure
// paths after `docker save` succeeded: the archive it wrote can't be
// loaded, and the temp dir must still be removed.
func TestLoadDockerDaemonCleansUpOnUnreadableArchive(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.tar")
	if err := os.WriteFile(garbage, []byte("not a tar archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	installFakeDocker(t, garbage)
	isolateTempDir(t)

	ref, err := source.ParseRef("docker-daemon:my-app:dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH}); err == nil {
		t.Fatalf("Load: expected an error for an unreadable docker save archive")
	}
	if got := tempDirsUnder(t); len(got) != 0 {
		t.Errorf("temp dir(s) %v left behind after a failed Load", got)
	}
}

func TestLoadDockerDaemonNamesBundleFromRef(t *testing.T) {
	archiveFixture, _ := buildDockerDaemonOCILayoutFixture(t)
	installFakeDocker(t, archiveFixture)
	isolateTempDir(t)

	for raw, want := range map[string]string{
		"docker-daemon:my-app":                                   "my-app/latest",
		"docker-daemon:localhost:5000/team/app:v2":               "app/v2",
		"docker-daemon:my-app@sha256:" + strings.Repeat("a", 64): "my-app/latest",
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

func TestParseRefDockerDaemonRejectsFlagLikeRef(t *testing.T) {
	for _, raw := range []string{"docker-daemon:-o", "docker-daemon:--output=/tmp/x", "docker-daemon:"} {
		if _, err := source.ParseRef(raw); err == nil {
			t.Errorf("ParseRef(%q) succeeded, want an error", raw)
		}
	}
}

func TestLoadDockerDaemonMissingDocker(t *testing.T) {
	// An empty PATH means exec.LookPath("docker") cannot find anything.
	t.Setenv("PATH", t.TempDir())

	ref, err := source.ParseRef("docker-daemon:my-app:dev")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: runtime.GOARCH})
	if err == nil {
		t.Fatalf("Load: expected an error when docker is not on PATH")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("error %q does not mention docker", err.Error())
	}
}

// isolateTempDir points TMPDIR at a fresh directory for the rest of the
// test, so tempDirsUnder only sees what this test created (not other
// packages' tests running in parallel, or leftovers from earlier runs),
// and returns it.
func isolateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

// tempDirsUnder lists the contemper-docker-daemon-* directories currently
// in os.TempDir(), for asserting on cleanup.
func tempDirsUnder(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "contemper-docker-daemon-") {
			matches = append(matches, filepath.Join(os.TempDir(), e.Name()))
		}
	}
	return matches
}
