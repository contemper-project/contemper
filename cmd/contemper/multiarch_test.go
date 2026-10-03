package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/bundle"
)

// groupFile is the group file the fixture archive's bundles are listed in.
const groupFile = "docker-save-latest.multiarch.json"

// runConvertCmd runs `contemper convert` with args against a fake
// qemu-img (the real ext4 host tools, which the caller must have checked
// for) and returns its stdout.
func runConvertCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("PATH", installFakeTool(t, "qemu-img", fastQemuImgScript)+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := newConvertCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SetArgs(append([]string{"--target", "qemu", "--quiet"}, args...))
	err := cmd.Execute()
	return stdout.String(), err
}

// runConvertCmdReport is runConvertCmd with the progress report on, in
// plain mode, and returns what the command wrote to stderr as well.
func runConvertCmdReport(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("PATH", installFakeTool(t, "qemu-img", fastQemuImgScript)+string(os.PathListSeparator)+os.Getenv("PATH"))
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stderr = orig }()

	cmd := newConvertCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SetArgs(append([]string{"--target", "qemu", "--progress", "plain"}, args...))
	err = cmd.Execute()
	_ = w.Close()
	os.Stderr = orig
	return out.String(), <-done, err
}

// lineCount returns how many lines of s contain sub.
func lineCount(s, sub string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func TestConvertAllArchitectures(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64", "amd64")

	for _, arch := range []string{"all", "arm64,amd64"} {
		outDir := t.TempDir()
		stdout, err := runConvertCmd(t, "--arch", arch, "-o", outDir, "oci-archive:"+archive)
		if err != nil {
			t.Fatalf("--arch %s: %v", arch, err)
		}
		want := filepath.Join(outDir, "docker-save-latest.x86_64") + "\n" + filepath.Join(outDir, "docker-save-latest.aarch64") + "\n"
		if stdout != want {
			t.Errorf("--arch %s stdout = %q, want %q", arch, stdout, want)
		}
		for dir, arch := range map[string]string{"docker-save-latest.x86_64": "amd64", "docker-save-latest.aarch64": "arm64"} {
			m, err := bundle.Read(filepath.Join(outDir, dir))
			if err != nil {
				t.Fatalf("reading %s: %v", dir, err)
			}
			if m.Arch != arch {
				t.Errorf("%s manifest arch = %q, want %q", dir, m.Arch, arch)
			}
		}

		g, err := bundle.ReadGroup(filepath.Join(outDir, groupFile))
		if err != nil {
			t.Fatalf("reading the group file: %v", err)
		}
		if len(g.Bundles) != 2 || g.Bundles[0] != (bundle.GroupBundle{Arch: "amd64", Path: "docker-save-latest.x86_64"}) ||
			g.Bundles[1] != (bundle.GroupBundle{Arch: "arm64", Path: "docker-save-latest.aarch64"}) {
			t.Errorf("group bundles = %+v", g.Bundles)
		}
		if g.Source.Ref != "oci-archive:"+archive || g.Source.Repo != "docker-save" {
			t.Errorf("group source = %+v", g.Source)
		}
	}
}

func TestConvertAllOnSinglePlatformSource(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64")
	outDir := t.TempDir()
	stdout, err := runConvertCmd(t, "--arch", "all", "-o", outDir, "oci-archive:"+archive)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(outDir, "docker-save-latest.aarch64") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	// Asking for all still yields a group file, so scripts get a stable artifact.
	g, err := bundle.ReadGroup(filepath.Join(outDir, groupFile))
	if err != nil || len(g.Bundles) != 1 || g.Bundles[0].Arch != "arm64" {
		t.Errorf("group file = %+v, %v", g, err)
	}
}

func TestConvertSingleArchWritesNoGroupFile(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64", "amd64")
	outDir := t.TempDir()
	stdout, err := runConvertCmd(t, "--arch", "arm64", "-o", outDir, "oci-archive:"+archive)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(outDir, "docker-save-latest.aarch64") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if names := tempDirEntries(t, outDir); len(names) != 1 {
		t.Errorf("out dir = %v, want just the one bundle", names)
	}
}

func TestConvertGroupRunReplacesStaleGroupFile(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64", "amd64")
	outDir := t.TempDir()
	stale := filepath.Join(outDir, groupFile)
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A failing run (the second arch's bundle path is blocked by a file)
	// must not leave the stale group file behind.
	if err := os.WriteFile(filepath.Join(outDir, "docker-save-latest.aarch64"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := runConvertCmd(t, "--arch", "all", "-o", outDir, "oci-archive:"+archive)
	if err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("error = %v, want a failure naming linux/arm64", err)
	}
	if want := filepath.Join(outDir, "docker-save-latest.x86_64") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want the finished amd64 bundle only", stdout)
	}
	if _, err := bundle.Read(filepath.Join(outDir, "docker-save-latest.x86_64")); err != nil {
		t.Errorf("the finished bundle was not kept: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale group file still present after a failed run: %v", err)
	}
}

func TestConvertSingleArchRunRemovesStaleGroupFile(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64", "amd64")
	outDir := t.TempDir()
	if _, err := runConvertCmd(t, "--arch", "all", "-o", outDir, "oci-archive:"+archive); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, groupFile)); err != nil {
		t.Fatalf("first run wrote no group file: %v", err)
	}
	// The tag has since moved: a run for one architecture replaces that
	// bundle, so the group file can't be left describing the old pair.
	_, stderr, err := runConvertCmdReport(t, "--arch", "amd64", "-o", outDir, "oci-archive:"+archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, groupFile)); !os.IsNotExist(err) {
		t.Errorf("group file still present after a single-architecture run: %v", err)
	}
	if !strings.Contains(stderr, "removed stale group file") || !strings.Contains(stderr, groupFile) {
		t.Errorf("stderr doesn't mention the removal:\n%s", stderr)
	}

	// With no group file there is nothing to report.
	_, stderr, err = runConvertCmdReport(t, "--arch", "amd64", "-o", outDir, "oci-archive:"+archive)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "stale group file") {
		t.Errorf("stderr mentions a removal that didn't happen:\n%s", stderr)
	}
}

func TestConvertResolveFailureLeavesGroupFile(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64")
	outDir := t.TempDir()
	old := filepath.Join(outDir, groupFile)
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The failure is at the index, before any image is loaded, so the
	// bundle (and group file) name isn't known yet.
	if _, err := runConvertCmd(t, "--arch", "amd64,arm64", "-o", outDir, "oci-archive:"+archive); err == nil {
		t.Fatal("expected a missing-architecture error")
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "old" {
		t.Errorf("group file = %q, %v; want it left alone", data, err)
	}
}

func TestConvertArchitecturesWithDifferentNamesRejected(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveNamed(t, map[string]string{"amd64": "alpha:v1", "arm64": "beta:v1"}, "amd64", "arm64")
	outDir := t.TempDir()
	stdout, err := runConvertCmd(t, "--arch", "all", "-o", outDir, "oci-archive:"+archive)
	if err == nil || !strings.Contains(err.Error(), "different bundle names (alpha-v1 and beta-v1)") || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("error = %v", err)
	}
	if want := filepath.Join(outDir, "alpha-v1.x86_64") + "\n"; stdout != want {
		t.Errorf("stdout = %q, want only the first bundle %q", stdout, want)
	}
	if _, err := os.Stat(filepath.Join(outDir, "alpha-v1.multiarch.json")); !os.IsNotExist(err) {
		t.Errorf("a group file was written for mismatched names: %v", err)
	}
}

func TestConvertListMissingArchFailsBeforeConverting(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64")
	outDir := t.TempDir()
	stdout, err := runConvertCmd(t, "--arch", "amd64,arm64", "-o", outDir, "oci-archive:"+archive)
	if err == nil || !strings.Contains(err.Error(), "no linux/amd64 platform") {
		t.Fatalf("error = %v, want a missing amd64 platform", err)
	}
	if stdout != "" || len(tempDirEntries(t, outDir)) != 0 {
		t.Errorf("stdout %q, out dir %v: want nothing produced", stdout, tempDirEntries(t, outDir))
	}
}

func TestConvertBadArchValues(t *testing.T) {
	// Rejected while parsing --arch, before the source is read.
	for _, c := range []struct{ arch, want string }{
		{"amd64,amd64", "listed more than once"},
		{"amd64,", "empty item"},
		{"riscv64", "unknown architecture"},
		{"all,arm64", "unknown architecture"},
		{" amd64", "unknown architecture"},
		{"amd64, arm64", "unknown architecture"},
		{"AMD64", "unknown architecture"},
		{"ALL", "unknown architecture"},
	} {
		_, err := runConvertCmd(t, "--arch", c.arch, "-o", t.TempDir(), "oci-archive:/nonexistent.tar")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--arch %q: error = %v, want one containing %q", c.arch, err, c.want)
		}
	}
}

func TestConvertSeveralArchitecturesOfDockerDaemonRejected(t *testing.T) {
	for _, arch := range []string{"all", "amd64,arm64"} {
		_, err := runConvertCmd(t, "--arch", arch, "-o", t.TempDir(), "docker-daemon:x")
		if err == nil || !strings.Contains(err.Error(), "docker-daemon: sources convert one architecture per run; pass --arch amd64 or --arch arm64") {
			t.Errorf("--arch %s: error = %v", arch, err)
		}
	}
}

func TestConvertMultiArchReport(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64", "amd64")
	stdout, stderr, err := runConvertCmdReport(t, "--arch", "all", "-o", t.TempDir(), "oci-archive:"+archive)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(stdout, "\n"); n != 2 || strings.Contains(stdout, "bundle ready") {
		t.Errorf("stdout = %q, want just the two bundle paths", stdout)
	}
	first, second := strings.Index(stderr, "architecture amd64"), strings.Index(stderr, "architecture arm64")
	if first < 0 || second < first {
		t.Errorf("want an amd64 then an arm64 header in:\n%s", stderr)
	}
	for _, want := range []string{"1 of 2", "2 of 2"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if n := lineCount(stderr, "bundle ready"); n != 2 {
		t.Errorf("%d bundle ready lines, want 2:\n%s", n, stderr)
	}
}

func TestConvertSingleArchReportHasNoArchitectureHeaders(t *testing.T) {
	requireExt4HostTools(t)
	archive := buildFixtureArchiveFor(t, "arm64", "amd64")
	_, stderr, err := runConvertCmdReport(t, "--arch", "arm64", "-o", t.TempDir(), "oci-archive:"+archive)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "architecture ") || strings.Contains(stderr, " of 2") {
		t.Errorf("single-architecture report has multi-arch headers:\n%s", stderr)
	}
	if n := lineCount(stderr, "bundle ready"); n != 1 {
		t.Errorf("%d bundle ready lines, want 1:\n%s", n, stderr)
	}
	for _, want := range []string{"linux/arm64", "contemper-ready"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}
