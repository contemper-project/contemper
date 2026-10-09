package source_test

import (
	"errors"
	"path/filepath"
	"reflect"
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

func TestParseArchSelection(t *testing.T) {
	cases := []struct {
		in      string
		want    source.ArchSelection
		wantErr string
	}{
		{in: "amd64", want: source.ArchSelection{Archs: []string{"amd64"}}},
		{in: "arm64", want: source.ArchSelection{Archs: []string{"arm64"}}},
		{in: "all", want: source.ArchSelection{All: true, Group: true}},
		{in: "arm64,amd64", want: source.ArchSelection{Archs: []string{"amd64", "arm64"}, Group: true}},
		{in: "amd64,arm64", want: source.ArchSelection{Archs: []string{"amd64", "arm64"}, Group: true}},
		{in: "amd64,amd64", wantErr: "more than once"},
		{in: "amd64,", wantErr: "empty item"},
		{in: ",arm64", wantErr: "empty item"},
		{in: "amd64,,arm64", wantErr: "empty item"},
		{in: "amd64,riscv64", wantErr: "unknown architecture"},
		{in: "all,amd64", wantErr: "unknown architecture"},
		{in: "bogus", wantErr: "unknown architecture"},
		// Values are matched exactly: no trimming, no case folding.
		{in: " amd64", wantErr: "unknown architecture"},
		{in: "amd64 ", wantErr: "unknown architecture"},
		{in: "amd64, arm64", wantErr: "unknown architecture"},
		{in: "AMD64", wantErr: "unknown architecture"},
		{in: "ALL", wantErr: "unknown architecture"},
	}
	for _, c := range cases {
		got, err := source.ParseArchSelection(c.in)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("ParseArchSelection(%q) error = %v, want one containing %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseArchSelection(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseArchSelection(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseArchSelectionEmptyIsHost(t *testing.T) {
	sel, err := source.ParseArchSelection("")
	if err != nil {
		t.Skipf("host architecture unsupported: %v", err)
	}
	host, _ := source.HostPlatform("")
	if sel.All || sel.Group || !reflect.DeepEqual(sel.Archs, []string{host.Architecture}) {
		t.Errorf("ParseArchSelection(\"\") = %+v, want the host architecture only", sel)
	}
}

func TestPlatformsOfLayout(t *testing.T) {
	dir := t.TempDir()
	buildLayout(t, dir)
	got, err := source.Platforms(t.Context(), source.Ref{Kind: source.KindOCILayout, Value: dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Platforms = %v, want %v", got, want)
	}
}

func TestPlatformsOfNestedLayoutIgnoresAttestations(t *testing.T) {
	dir := t.TempDir()
	buildNestedLayout(t, dir, nil)
	got, err := source.Platforms(t.Context(), source.Ref{Kind: source.KindOCILayout, Value: dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Platforms = %v, want %v", got, want)
	}
}

func TestPlatformsOfDockerDaemonUnknown(t *testing.T) {
	_, err := source.Platforms(t.Context(), source.Ref{Kind: source.KindDockerDaemon, Value: "x:y"})
	if !errors.Is(err, source.ErrPlatformsUnknown) {
		t.Errorf("Platforms(docker-daemon) error = %v, want ErrPlatformsUnknown", err)
	}
}

func TestPlatformsOfContainersStorageUnknown(t *testing.T) {
	_, err := source.Platforms(t.Context(), source.Ref{Kind: source.KindContainersStorage, Value: "x:y"})
	if !errors.Is(err, source.ErrPlatformsUnknown) {
		t.Errorf("Platforms(containers-storage) error = %v, want ErrPlatformsUnknown", err)
	}
}

func TestArchSelectionResolve(t *testing.T) {
	dir := t.TempDir()
	buildLayout(t, dir)
	ref := source.Ref{Kind: source.KindOCILayout, Value: dir}

	archs := func(ps []v1.Platform) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Architecture)
		}
		return out
	}
	resolve := func(in string) ([]string, error) {
		sel, err := source.ParseArchSelection(in)
		if err != nil {
			t.Fatal(err)
		}
		ps, err := sel.Resolve(t.Context(), ref)
		return archs(ps), err
	}

	if got, err := resolve("all"); err != nil || !reflect.DeepEqual(got, []string{"amd64", "arm64"}) {
		t.Errorf("all = %v, %v", got, err)
	}
	if got, err := resolve("arm64,amd64"); err != nil || !reflect.DeepEqual(got, []string{"amd64", "arm64"}) {
		t.Errorf("list = %v, %v", got, err)
	}
	if got, err := resolve("arm64"); err != nil || !reflect.DeepEqual(got, []string{"arm64"}) {
		t.Errorf("single = %v, %v", got, err)
	}

	// A single-platform layout: all converts that one platform, a list
	// naming a missing one fails at the index.
	single := t.TempDir()
	buildSingleArchLayout(t, single, "arm64")
	sref := source.Ref{Kind: source.KindOCILayout, Value: single}
	sel, _ := source.ParseArchSelection("all")
	ps, err := sel.Resolve(t.Context(), sref)
	if err != nil || len(ps) != 1 || ps[0].Architecture != "arm64" {
		t.Errorf("all on single-arch source = %v, %v", ps, err)
	}
	sel, _ = source.ParseArchSelection("amd64,arm64")
	if _, err := sel.Resolve(t.Context(), sref); err == nil || !strings.Contains(err.Error(), "no linux/amd64 platform") {
		t.Errorf("list with missing arch error = %v", err)
	}

	// Nothing supported at all.
	none := t.TempDir()
	buildSingleArchLayout(t, none, "riscv64")
	sel, _ = source.ParseArchSelection("all")
	if _, err := sel.Resolve(t.Context(), source.Ref{Kind: source.KindOCILayout, Value: none}); err == nil || !strings.Contains(err.Error(), "linux/riscv64") {
		t.Errorf("all with no supported platform error = %v", err)
	}

	// docker-daemon sources can't be listed: one arch is fine, a set is not.
	dref := source.Ref{Kind: source.KindDockerDaemon, Value: "x:y"}
	sel, _ = source.ParseArchSelection("all")
	if _, err := sel.Resolve(t.Context(), dref); err == nil || !strings.Contains(err.Error(), "docker-daemon: sources convert one architecture per run; pass --arch amd64 or --arch arm64") {
		t.Errorf("all on docker-daemon error = %v", err)
	}
	sel, _ = source.ParseArchSelection("amd64,arm64")
	if _, err := sel.Resolve(t.Context(), dref); err == nil || !strings.Contains(err.Error(), "one architecture per run") {
		t.Errorf("list on docker-daemon error = %v", err)
	}
	sel, _ = source.ParseArchSelection("amd64")
	if ps, err := sel.Resolve(t.Context(), dref); err != nil || len(ps) != 1 {
		t.Errorf("single arch on docker-daemon = %v, %v", ps, err)
	}
}

func TestArchSelectionResolveContainersStorage(t *testing.T) {
	cref := source.Ref{Kind: source.KindContainersStorage, Value: "x:y"}
	for _, arch := range []string{"all", "amd64,arm64"} {
		sel, _ := source.ParseArchSelection(arch)
		if _, err := sel.Resolve(t.Context(), cref); err == nil || !strings.Contains(err.Error(), "containers-storage: sources convert one architecture per run; pass --arch amd64 or --arch arm64") {
			t.Errorf("%s on containers-storage error = %v", arch, err)
		}
	}
	sel, _ := source.ParseArchSelection("arm64")
	if ps, err := sel.Resolve(t.Context(), cref); err != nil || len(ps) != 1 {
		t.Errorf("single arch on containers-storage = %v, %v", ps, err)
	}
}

// buildSingleArchLayout writes a one-image OCI layout whose descriptor
// carries no platform metadata, so the image config says what it is.
func buildSingleArchLayout(t *testing.T, dir, arch string) {
	t.Helper()
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: arch}, nil,
		[]imgtest.File{{Path: "marker", Data: []byte(arch)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layout.Write(dir, mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})); err != nil {
		t.Fatal(err)
	}
}

// blankPlatform returns img with its config's OS and architecture
// cleared, like a legacy docker-archive or an image built without them.
func blankPlatform(t *testing.T, img v1.Image) v1.Image {
	t.Helper()
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "", ""
	out, err := mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlatformsOfSingleImageWithoutPlatformInfo(t *testing.T) {
	ctx := t.Context()
	img := blankPlatform(t, testImage(t, "amd64"))

	dir := t.TempDir()
	if _, err := layout.Write(dir, mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})); err != nil {
		t.Fatal(err)
	}
	ref := source.Ref{Kind: source.KindOCILayout, Value: dir}
	if _, err := source.Platforms(ctx, ref); !errors.Is(err, source.ErrPlatformsUnknown) {
		t.Fatalf("Platforms = %v, want ErrPlatformsUnknown", err)
	}
	// Load accepts it for any one architecture, so exactly one explicit
	// architecture is fine and a set is rejected with a clear message.
	if _, err := source.Load(ctx, ref, v1.Platform{OS: "linux", Architecture: "arm64"}); err != nil {
		t.Errorf("Load(arm64): %v", err)
	}
	sel, _ := source.ParseArchSelection("amd64")
	if ps, err := sel.Resolve(ctx, ref); err != nil || len(ps) != 1 {
		t.Errorf("Resolve(amd64) = %v, %v", ps, err)
	}
	for _, in := range []string{"all", "amd64,arm64"} {
		sel, _ := source.ParseArchSelection(in)
		if _, err := sel.Resolve(ctx, ref); err == nil || !strings.Contains(err.Error(), "no platform metadata") {
			t.Errorf("Resolve(%s) error = %v", in, err)
		}
	}

	// A legacy docker-archive is the same story.
	tarPath := filepath.Join(t.TempDir(), "legacy.tar")
	tag, _ := name.NewTag("example/legacy:v1")
	if err := tarball.WriteToFile(tarPath, tag, img); err != nil {
		t.Fatal(err)
	}
	dref := source.Ref{Kind: source.KindDockerArchive, Value: tarPath}
	if _, err := source.Platforms(ctx, dref); !errors.Is(err, source.ErrPlatformsUnknown) {
		t.Errorf("Platforms(docker-archive) = %v, want ErrPlatformsUnknown", err)
	}
	if l, err := source.Load(ctx, dref, v1.Platform{OS: "linux", Architecture: "arm64"}); err != nil {
		t.Errorf("Load(docker-archive, arm64): %v", err)
	} else {
		l.Close()
	}
}

func TestPlatformsOfDockerArchiveWithPlatform(t *testing.T) {
	tarPath := filepath.Join(t.TempDir(), "img.tar")
	tag, _ := name.NewTag("example/img:v1")
	if err := tarball.WriteToFile(tarPath, tag, testImage(t, "arm64")); err != nil {
		t.Fatal(err)
	}
	checkPlatformsMatchLoad(t, source.Ref{Kind: source.KindDockerArchive, Value: tarPath}, []string{"arm64"})
}

func TestPlatformsOfSeveralImagesWithoutPlatformInfo(t *testing.T) {
	dir := t.TempDir()
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: testImage(t, "amd64")},
		mutate.IndexAddendum{Add: testImage(t, "arm64")})
	if _, err := layout.Write(dir, idx); err != nil {
		t.Fatal(err)
	}
	ref := source.Ref{Kind: source.KindOCILayout, Value: dir}
	_, err := source.Platforms(t.Context(), ref)
	if err == nil || errors.Is(err, source.ErrPlatformsUnknown) || !strings.Contains(err.Error(), "without platform metadata") {
		t.Errorf("Platforms error = %v", err)
	}
	// Load rejects it too.
	if _, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: "amd64"}); err == nil {
		t.Error("Load accepted a layout of several images without platform metadata")
	}
}

func TestLoadSingleImageMustMatchRequestedPlatform(t *testing.T) {
	dir := t.TempDir()
	buildSingleArchLayout(t, dir, "arm64")
	if _, err := source.Load(t.Context(), source.Ref{Kind: source.KindOCILayout, Value: dir}, v1.Platform{OS: "linux", Architecture: "amd64"}); err == nil || !strings.Contains(err.Error(), "linux/arm64, want linux/amd64") {
		t.Errorf("Load error = %v", err)
	}
}

func TestPlatformsOfOCIArchive(t *testing.T) {
	dir := t.TempDir()
	buildNestedLayout(t, dir, nil)
	ref := source.Ref{Kind: source.KindOCIArchive, Value: tarLayout(t, dir, "multi.tar")}
	checkPlatformsMatchLoad(t, ref, []string{"amd64", "arm64"})
}
