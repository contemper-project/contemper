package source_test

import (
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/source"
)

func TestParseRef(t *testing.T) {
	cases := []struct {
		raw      string
		wantKind source.Kind
		wantVal  string
		wantErr  bool
	}{
		{"ghcr.io/example/app:v1", source.KindRegistry, "ghcr.io/example/app:v1", false},
		{"oci-archive:/tmp/x.tar", source.KindOCIArchive, "/tmp/x.tar", false},
		{"oci:/tmp/layout", source.KindOCILayout, "/tmp/layout", false},
		{"docker-archive:/tmp/x.tar", source.KindDockerArchive, "/tmp/x.tar", false},
		{"", "", "", true},
		{"oci-archive:", "", "", true},
	}
	for _, c := range cases {
		ref, err := source.ParseRef(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRef(%q): expected error", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRef(%q): %v", c.raw, err)
			continue
		}
		if ref.Kind != c.wantKind || ref.Value != c.wantVal {
			t.Errorf("ParseRef(%q) = %+v, want {%s %s}", c.raw, ref, c.wantKind, c.wantVal)
		}
	}
}

func TestCheckReady(t *testing.T) {
	cases := []struct {
		name    string
		labels  map[string]string
		wantErr bool
	}{
		{"missing label", nil, true},
		{"false label", map[string]string{"io.contemper.ready": "false"}, true},
		{"true label", map[string]string{"io.contemper.ready": "true"}, false},
	}
	for _, c := range cases {
		cfg := &v1.ConfigFile{}
		cfg.Config.Labels = c.labels
		err := source.CheckReady(cfg)
		if c.wantErr != (err != nil) {
			t.Errorf("%s: CheckReady error = %v, wantErr %v", c.name, err, c.wantErr)
		}
	}
}

func TestHostPlatform(t *testing.T) {
	if _, err := source.HostPlatform("bogus"); err == nil {
		t.Errorf("HostPlatform(bogus): expected error")
	}
	p, err := source.HostPlatform("arm64")
	if err != nil || p.OS != "linux" || p.Architecture != "arm64" {
		t.Errorf("HostPlatform(arm64) = %+v, %v", p, err)
	}
}

// buildLayout writes a two-platform OCI layout (arm64 + amd64) to dir,
// tagging the amd64 manifest with a ref-name annotation, and returns the
// digest of each platform's image for later comparison.
func buildLayout(t *testing.T, dir string) (arm64Digest, amd64Digest v1.Hash) {
	t.Helper()

	arm64Img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "arm64"}, nil,
		[]imgtest.File{{Path: "arm64-marker", Data: []byte("arm64")}})
	if err != nil {
		t.Fatal(err)
	}
	amd64Img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "amd64"}, nil,
		[]imgtest.File{{Path: "amd64-marker", Data: []byte("amd64")}})
	if err != nil {
		t.Fatal(err)
	}

	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add: arm64Img,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: "linux", Architecture: "arm64"},
			},
		},
		mutate.IndexAddendum{
			Add: amd64Img,
			Descriptor: v1.Descriptor{
				Platform:    &v1.Platform{OS: "linux", Architecture: "amd64"},
				Annotations: map[string]string{source.RefNameAnnotation: "example:dev"},
			},
		},
	)
	if _, err := layout.Write(dir, idx); err != nil {
		t.Fatalf("writing layout: %v", err)
	}

	arm64Digest, err = arm64Img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	amd64Digest, err = amd64Img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return arm64Digest, amd64Digest
}

func TestLoadOCILayoutSelectsPlatform(t *testing.T) {
	dir := t.TempDir()
	_, amd64Digest := buildLayout(t, dir)

	img, err := source.Load(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Digest != amd64Digest {
		t.Errorf("Load selected digest %s, want %s", img.Digest, amd64Digest)
	}
	if img.RepoBase != "example" || img.Tag != "dev" {
		t.Errorf("RepoBase/Tag = %s/%s, want example/dev", img.RepoBase, img.Tag)
	}
	if img.Reproducible {
		t.Errorf("layout sources should be marked non-reproducible")
	}
}

func TestLoadOCILayoutWrongArch(t *testing.T) {
	dir := t.TempDir()
	buildLayout(t, dir)

	_, err := source.Load(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "riscv64"})
	if err == nil {
		t.Fatalf("Load: expected an error for an unmatched platform")
	}
}

func TestIndexAnnotationsFallsBackToDescriptor(t *testing.T) {
	dir := t.TempDir()
	buildLayout(t, dir) // tags the amd64 manifest's index descriptor with RefNameAnnotation

	anns, err := source.IndexAnnotations(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("IndexAnnotations: %v", err)
	}
	if anns[source.RefNameAnnotation] != "example:dev" {
		t.Errorf("IndexAnnotations = %v, want %s=example:dev", anns, source.RefNameAnnotation)
	}

	// The arm64 descriptor in the same index carries no such annotation.
	anns, err = source.IndexAnnotations(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "arm64"})
	if err != nil {
		t.Fatalf("IndexAnnotations: %v", err)
	}
	if _, ok := anns[source.RefNameAnnotation]; ok {
		t.Errorf("arm64 descriptor should carry no ref-name annotation, got %v", anns)
	}
}

func TestRefStringCleansLocalPaths(t *testing.T) {
	for raw, want := range map[string]string{
		"oci-archive:/a/dev/../contemper/_out/x.tar": "oci-archive:/a/contemper/_out/x.tar",
		"oci:./layout/":          "oci:layout",
		"ghcr.io/example/app:v1": "ghcr.io/example/app:v1",
	} {
		ref, err := source.ParseRef(raw)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", raw, err)
		}
		if got := ref.String(); got != want {
			t.Errorf("ParseRef(%q).String() = %q, want %q", raw, got, want)
		}
	}
}
