package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/bundle"
)

// realTempDir returns t.TempDir() with symlinks resolved, since
// resolveDeployBundle resolves them in a group file's path (on macOS the
// temp dir is under /var, a symlink to /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// deployFixture writes one bundle per arch under a temp dir, with a
// group file listing them (manifestArch overrides what a bundle's own
// manifest says, to model stale files), and returns the directory and
// the group file's path.
func deployFixture(t *testing.T, listed []string, manifestArch map[string]string) (dir, group string) {
	t.Helper()
	dir = realTempDir(t)
	g := &bundle.Group{FormatVersion: bundle.GroupFormatVersion}
	for _, a := range listed {
		name := bundleDirName(a)
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		ma := a
		if o, ok := manifestArch[a]; ok {
			ma = o
		}
		m := &bundle.Manifest{FormatVersion: 1, Arch: ma, Disk: bundle.DiskInfo{File: "disk.qcow2", Format: "qcow2"}}
		if err := bundle.Write(filepath.Join(dir, name), m); err != nil {
			t.Fatal(err)
		}
		g.Bundles = append(g.Bundles, bundle.GroupBundle{Arch: a, Path: name})
	}
	var err error
	group, err = bundle.WriteGroup(dir, "app-dev", g)
	if err != nil {
		t.Fatal(err)
	}
	return dir, group
}

// bundleDirName is the directory name convert gives an architecture's
// bundle, e.g. app-dev.aarch64.
func bundleDirName(arch string) string { return "app-dev." + machineArch(arch) }

func hostArch(t *testing.T) (host, other string) {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64":
		return "amd64", "arm64"
	case "arm64":
		return "arm64", "amd64"
	}
	t.Skipf("unsupported host architecture %s", runtime.GOARCH)
	return "", ""
}

func TestResolveDeployGroupPicksHostArch(t *testing.T) {
	host, _ := hostArch(t)
	dir, group := deployFixture(t, []string{"amd64", "arm64"}, nil)
	got, m, chosen, err := resolveDeployBundle(group, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, bundleDirName(host)); got != want || m.Arch != host {
		t.Errorf("got %s (%s), want %s", got, m.Arch, want)
	}
	if want := host + ", matches host"; chosen != want {
		t.Errorf("chosen = %q, want %q", chosen, want)
	}
}

func TestResolveDeployGroupArchFlagSelectsOther(t *testing.T) {
	_, other := hostArch(t)
	dir, group := deployFixture(t, []string{"amd64", "arm64"}, nil)
	got, m, chosen, err := resolveDeployBundle(group, other)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, bundleDirName(other)); got != want || m.Arch != other {
		t.Errorf("got %s (%s), want %s", got, m.Arch, want)
	}
	if want := other + ", from --arch"; chosen != want {
		t.Errorf("chosen = %q, want %q", chosen, want)
	}
}

func TestResolveDeployGroupMissingHostArch(t *testing.T) {
	host, other := hostArch(t)
	_, group := deployFixture(t, []string{other}, nil)
	_, _, _, err := resolveDeployBundle(group, "")
	if err == nil || !strings.Contains(err.Error(), "no bundle for "+host) ||
		!strings.Contains(err.Error(), "available: "+other) || !strings.Contains(err.Error(), "--arch") {
		t.Fatalf("err = %v, want missing-arch error listing %s and suggesting --arch", err, other)
	}
	// An explicit --arch the group lacks is an error too.
	_, _, _, err = resolveDeployBundle(group, host)
	if err == nil || !strings.Contains(err.Error(), "no bundle for "+host) {
		t.Fatalf("err = %v, want missing-arch error", err)
	}
}

func TestResolveDeployGroupManifestMismatch(t *testing.T) {
	host, other := hostArch(t)
	_, group := deployFixture(t, []string{host}, map[string]string{host: other})
	_, _, _, err := resolveDeployBundle(group, "")
	if err == nil || !strings.Contains(err.Error(), "manifest says "+other) {
		t.Fatalf("err = %v, want manifest mismatch", err)
	}
}

func TestResolveDeployPlainBundleDir(t *testing.T) {
	_, other := hostArch(t)
	dir, _ := deployFixture(t, []string{other}, nil)
	bdir := filepath.Join(dir, bundleDirName(other))

	// No --arch: any architecture boots (a foreign one under emulation).
	got, m, chosen, err := resolveDeployBundle(bdir, "")
	if err != nil || got != bdir || m.Arch != other || chosen != "" {
		t.Fatalf("got %s %v %q %v", got, m, chosen, err)
	}
	// A matching --arch is fine, a different one is an error.
	if _, _, _, err := resolveDeployBundle(bdir, other); err != nil {
		t.Fatal(err)
	}
	host, _ := hostArch(t)
	if _, _, _, err := resolveDeployBundle(bdir, host); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want arch mismatch", err)
	}
}

func TestResolveDeployBadArch(t *testing.T) {
	_, group := deployFixture(t, []string{"amd64"}, nil)
	_, _, _, err := resolveDeployBundle(group, "s390x")
	if err == nil || !strings.Contains(err.Error(), "s390x") {
		t.Fatalf("err = %v, want an error naming the unsupported --arch", err)
	}
}

func TestResolveDeployNotABundleOrGroup(t *testing.T) {
	_, other := hostArch(t)
	dir, _ := deployFixture(t, []string{other}, nil)
	bdir := filepath.Join(dir, bundleDirName(other))

	manifest := filepath.Join(bdir, bundle.ManifestFile)
	_, _, _, err := resolveDeployBundle(manifest, "")
	if err == nil || !strings.Contains(err.Error(), "bundle directory "+bdir) {
		t.Fatalf("manifest: err = %v, want a pointer to %s", err, bdir)
	}

	disk := filepath.Join(bdir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("not a group"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = resolveDeployBundle(disk, "")
	if err == nil || !strings.Contains(err.Error(), "neither a bundle directory nor a <name>"+bundle.GroupSuffix) {
		t.Fatalf("disk: err = %v, want neither-bundle-nor-group error", err)
	}

	if _, _, _, err = resolveDeployBundle(filepath.Join(dir, "missing"), ""); !os.IsNotExist(err) {
		t.Fatalf("missing: err = %v, want not-exist", err)
	}
}

func TestResolveDeploySymlinkedGroupFile(t *testing.T) {
	host, _ := hostArch(t)
	dir, group := deployFixture(t, []string{"amd64", "arm64"}, nil)
	link := filepath.Join(t.TempDir(), filepath.Base(group))
	if err := os.Symlink(group, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, m, _, err := resolveDeployBundle(link, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, bundleDirName(host)); got != want || m.Arch != host {
		t.Errorf("got %s (%s), want %s", got, m.Arch, want)
	}
}

func TestResolveDeployGroupNestedEntry(t *testing.T) {
	host, _ := hostArch(t)
	dir := realTempDir(t)
	rel := "sub/" + bundleDirName(host)
	if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &bundle.Manifest{FormatVersion: 1, Arch: host, Disk: bundle.DiskInfo{File: "disk.qcow2", Format: "qcow2"}}
	if err := bundle.Write(filepath.Join(dir, filepath.FromSlash(rel)), m); err != nil {
		t.Fatal(err)
	}
	g := &bundle.Group{FormatVersion: bundle.GroupFormatVersion, Bundles: []bundle.GroupBundle{{Arch: host, Path: rel}}}
	group, err := bundle.WriteGroup(dir, "app-dev", g)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, err := resolveDeployBundle(group, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "sub", bundleDirName(host)); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestResolveDeployGroupListedBundleMissing(t *testing.T) {
	host, _ := hostArch(t)
	dir, group := deployFixture(t, []string{host}, nil)
	if err := os.RemoveAll(filepath.Join(dir, bundleDirName(host))); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := resolveDeployBundle(group, ""); err == nil {
		t.Fatal("want error for a listed bundle directory that is missing")
	}
}

func TestDeployCapsManifestVolumeSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "disk.raw"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &bundle.Manifest{
		FormatVersion: 1, Arch: "amd64",
		Disk:    bundle.DiskInfo{File: "disk.raw", Format: "raw"},
		Volumes: []bundle.Volume{{Name: "data", Path: "/data", SizeBytes: 2 << 40, FS: "ext4"}},
	}
	if err := bundle.Write(dir, m); err != nil {
		t.Fatal(err)
	}
	opts := deployOptions{bundleDir: dir, to: "local-qemu", quiet: true, progressMode: "plain", name: "inst"}
	err := runDeploy(t.Context(), nil, opts)
	if err == nil || !strings.Contains(err.Error(), "--volume") || !strings.Contains(err.Error(), "/data") {
		t.Fatalf("err = %v, want a refusal pointing at --volume", err)
	}
}
