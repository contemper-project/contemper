package volumehelper_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// generatorScript is the systemd variant's boot-time generator: it
// restores the Before=local-fs.target ordering nofail removes from a
// declared volume's fstab-generated mount unit (see the script's own
// comment, and docs/guide/volumes.md, for why). It reads
// /etc/contemper/volumes the same way format-volumes does, so these
// tests exercise the real script - not a copy hand-maintained here -
// against a temporary volumes file and a fake systemd-escape.
const generatorScript = "../../support/volumes-support/systemd/etc/systemd/system-generators/contemper-volumes"

// fakeSystemdEscape writes a minimal systemd-escape shim to
// binDir/systemd-escape, covering only the "--path --suffix=mount
// <path>" form the generator uses. Its output for /data and
// /var/lib/my-app matches what the real systemd-escape produces
// (data.mount, var-lib-my\x2dapp.mount - "-" inside a path component is
// escaped, since "-" is also how "/" is rendered in a unit name), so
// TestGeneratorWritesDropins exercises real escaping without requiring
// systemd-escape to be installed on the host running `go test`.
func fakeSystemdEscape(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
suffix=
path=
for a in "$@"; do
	case "$a" in
	--path) ;;
	--suffix=*) suffix=${a#--suffix=} ;;
	*) path=$a ;;
	esac
done
case "$path" in
/data) name=data ;;
/var/lib/my-app) name='var-lib-my\x2dapp' ;;
*) exit 1 ;;
esac
if [ -n "$suffix" ]; then
	printf '%s.%s\n' "$name" "$suffix"
else
	printf '%s\n' "$name"
fi
`
	if err := os.WriteFile(filepath.Join(binDir, "systemd-escape"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake systemd-escape: %v", err)
	}
}

// writeVolumesFile writes contents to a temp file and returns its path,
// standing in for /etc/contemper/volumes: the generator's own
// CONTEMPER_VOLUMES_FILE override (test-only; the real path is fixed)
// points at it instead.
func writeVolumesFile(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "volumes")
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// runGenerator runs the real generator script with sh, pointed at
// volumesFile and with PATH set to path (which the caller controls, so
// a test can omit systemd-escape entirely), and returns the normal-dir
// it was given - a generator's usual output location
// (systemd.generator(7)) - for the test to inspect. It fails the test
// if the script exits nonzero or writes anything to stdout/stderr,
// since generators must not (systemd.generator(7)) and this one is
// documented to stay silent.
func runGenerator(t *testing.T, volumesFile, path string) (normalDir string) {
	t.Helper()
	dir := t.TempDir()
	normalDir = filepath.Join(dir, "normal")
	earlyDir := filepath.Join(dir, "early")
	lateDir := filepath.Join(dir, "late")
	for _, d := range []string{normalDir, earlyDir, lateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	abs, err := filepath.Abs(generatorScript)
	if err != nil {
		t.Fatal(err)
	}

	env := []string{"CONTEMPER_VOLUMES_FILE=" + volumesFile}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+path)

	cmd := exec.CommandContext(context.Background(), "sh", abs, normalDir, earlyDir, lateDir)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running generator: %v\n%s", err, out)
	}
	if len(out) != 0 {
		t.Errorf("generator wrote to stdout/stderr (it must not): %q", out)
	}
	return normalDir
}

// assertEmpty fails the test unless dir has no entries: the generator's
// "never break boot" contract means every failure mode - no volumes
// file, no systemd-escape - must produce no drop-ins at all, not a
// partial or malformed one.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected no drop-ins, got %v", names)
	}
}

func TestGeneratorWritesDropins(t *testing.T) {
	binDir := t.TempDir()
	fakeSystemdEscape(t, binDir)
	volumesFile := writeVolumesFile(t, ""+
		"data serial-data ext4 /data\n"+
		"myapp serial-myapp ext4 /var/lib/my-app\n")

	normalDir := runGenerator(t, volumesFile, binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cases := []struct {
		unit  string
		mount string
	}{
		{"data.mount", "/data"},
		{`var-lib-my\x2dapp.mount`, "/var/lib/my-app"},
	}
	for _, c := range cases {
		p := filepath.Join(normalDir, c.unit+".d", "contemper-volumes.conf")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		got := string(data)
		if !strings.Contains(got, "[Unit]") {
			t.Errorf("%s: expected a [Unit] section:\n%s", p, got)
		}
		if !strings.Contains(got, "Before=local-fs.target") {
			t.Errorf("%s: expected Before=local-fs.target:\n%s", p, got)
		}
		if !strings.Contains(got, c.mount) {
			t.Errorf("%s: expected a mention of %s:\n%s", p, c.mount, got)
		}
	}
}

func TestGeneratorMissingVolumesFile(t *testing.T) {
	binDir := t.TempDir()
	fakeSystemdEscape(t, binDir)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	normalDir := runGenerator(t, missing, binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	assertEmpty(t, normalDir)
}

func TestGeneratorMissingSystemdEscape(t *testing.T) {
	// An empty PATH: no systemd-escape (and nothing else) reachable, the
	// same as booting an image built without systemd, or any other
	// environment where the tool the generator depends on isn't there.
	volumesFile := writeVolumesFile(t, "data serial-data ext4 /data\n")
	emptyBin := t.TempDir()

	normalDir := runGenerator(t, volumesFile, emptyBin)

	assertEmpty(t, normalDir)
}
