package source_test

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// buildNestedLayout writes a two-level OCI layout to dir, mimicking what
// Docker's containerd image store writes for `docker save`: index.json
// points to a second index (tagged with outerAnnotations), which lists
// an arm64 manifest, an amd64 manifest (tagged with
// "io.contemper.leaf-marker": "amd64", standing in for a support-image
// annotation read at that level), and an attestation manifest -
// platform unknown/unknown, "vnd.docker.reference.type":
// "attestation-manifest" - that must be ignored throughout. It returns
// the digest of each platform's image.
func buildNestedLayout(t *testing.T, dir string, outerAnnotations map[string]string) (arm64Digest, amd64Digest v1.Hash) {
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
	attestationImg, err := imgtest.Image(v1.Platform{OS: "unknown", Architecture: "unknown"}, nil,
		[]imgtest.File{{Path: "attestation", Data: []byte("not a real platform image")}})
	if err != nil {
		t.Fatal(err)
	}

	innerIdx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add:        arm64Img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}},
		},
		mutate.IndexAddendum{
			Add: amd64Img,
			Descriptor: v1.Descriptor{
				Platform:    &v1.Platform{OS: "linux", Architecture: "amd64"},
				Annotations: map[string]string{"io.contemper.leaf-marker": "amd64"},
			},
		},
		mutate.IndexAddendum{
			Add: attestationImg,
			Descriptor: v1.Descriptor{
				Platform:    &v1.Platform{OS: "unknown", Architecture: "unknown"},
				Annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest"},
			},
		},
	)

	outerIdx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add:        innerIdx,
			Descriptor: v1.Descriptor{Annotations: outerAnnotations},
		},
	)

	if _, err := layout.Write(dir, outerIdx); err != nil {
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

// tarLayout tars the OCI layout directory dir into a new archive file
// named base (e.g. "example.tar") in a fresh temp dir, and returns its
// path - the oci-archive: form of a layout tests otherwise exercise
// directly as oci:.
func tarLayout(t *testing.T, dir, base string) string {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), base)
	out, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	tw := tar.NewWriter(out)
	defer func() { _ = tw.Close() }()

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
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
		f, err := os.Open(path)
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

func TestLoadOCILayoutNestedIndexPrefersContainerdName(t *testing.T) {
	dir := t.TempDir()
	_, amd64Digest := buildNestedLayout(t, dir, map[string]string{
		source.ContainerdNameAnnotation: "docker.io/library/contemper-example:dev",
		// Docker's containerd image store sets this to the bare tag,
		// which must not override the full name above and must not be
		// misread as a repo name with an implied "latest" tag.
		source.RefNameAnnotation: "dev",
	})

	img, err := source.Load(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Digest != amd64Digest {
		t.Errorf("Load selected digest %s, want %s", img.Digest, amd64Digest)
	}
	if img.RepoBase != "contemper-example" || img.Tag != "dev" {
		t.Errorf("RepoBase/Tag = %s/%s, want contemper-example/dev", img.RepoBase, img.Tag)
	}
}

func TestLoadOCIArchiveNestedIndexBareRefNameFallsBackToArchiveName(t *testing.T) {
	dir := t.TempDir()
	_, amd64Digest := buildNestedLayout(t, dir, map[string]string{
		// No io.containerd.image.name here: only the bare tag Docker
		// sets on org.opencontainers.image.ref.name.
		source.RefNameAnnotation: "dev",
	})
	archivePath := tarLayout(t, dir, "myimage-save.tar")

	img, err := source.Load(source.Ref{Kind: source.KindOCIArchive, Value: archivePath},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.Digest != amd64Digest {
		t.Errorf("Load selected digest %s, want %s", img.Digest, amd64Digest)
	}
	if img.RepoBase != "myimage-save" || img.Tag != "dev" {
		t.Errorf("RepoBase/Tag = %s/%s, want myimage-save/dev", img.RepoBase, img.Tag)
	}
}

func TestLoadOCIArchiveNoAnnotationsFallsBackToArchiveName(t *testing.T) {
	dir := t.TempDir()
	buildNestedLayout(t, dir, nil) // buildx --output type=oci: no naming annotation at all
	archivePath := tarLayout(t, dir, "example.tar")

	img, err := source.Load(source.Ref{Kind: source.KindOCIArchive, Value: archivePath},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if img.RepoBase != "example" || img.Tag != "latest" {
		t.Errorf("RepoBase/Tag = %s/%s, want example/latest", img.RepoBase, img.Tag)
	}
}

func TestLoadOCILayoutNestedIndexSelectsPlatformAndIgnoresAttestation(t *testing.T) {
	dir := t.TempDir()
	arm64Digest, amd64Digest := buildNestedLayout(t, dir, nil)

	img, err := source.Load(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "arm64"})
	if err != nil {
		t.Fatalf("Load(arm64): %v", err)
	}
	if img.Digest != arm64Digest {
		t.Errorf("Load(arm64) selected digest %s, want %s", img.Digest, arm64Digest)
	}

	img, err = source.Load(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("Load(amd64): %v", err)
	}
	if img.Digest != amd64Digest {
		t.Errorf("Load(amd64) selected digest %s, want %s", img.Digest, amd64Digest)
	}
}

func TestLoadOCILayoutNestedIndexWrongArch(t *testing.T) {
	dir := t.TempDir()
	buildNestedLayout(t, dir, nil)

	_, err := source.Load(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "riscv64"})
	if err == nil {
		t.Fatalf("Load: expected an error for an unmatched platform")
	}
}

func TestIndexAnnotationsNestedIndexReturnsLeafAnnotations(t *testing.T) {
	dir := t.TempDir()
	buildNestedLayout(t, dir, nil)

	anns, err := source.IndexAnnotations(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatalf("IndexAnnotations: %v", err)
	}
	if anns["io.contemper.leaf-marker"] != "amd64" {
		t.Errorf("IndexAnnotations = %v, want io.contemper.leaf-marker=amd64", anns)
	}

	anns, err = source.IndexAnnotations(source.Ref{Kind: source.KindOCILayout, Value: dir},
		v1.Platform{OS: "linux", Architecture: "arm64"})
	if err != nil {
		t.Fatalf("IndexAnnotations: %v", err)
	}
	if _, ok := anns["io.contemper.leaf-marker"]; ok {
		t.Errorf("arm64 leaf descriptor should carry no leaf-marker annotation, got %v", anns)
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

func TestParseVariantRef(t *testing.T) {
	registry := source.Ref{Kind: source.KindRegistry, Value: "ghcr.io/example/support:v1"}
	local := source.Ref{Kind: source.KindOCIArchive, Value: "support.tar"}

	for _, raw := range []string{"oci-archive:/home/user/private.tar", "oci:/var/lib/layouts/x", "docker-archive:/tmp/x.tar"} {
		if _, err := source.ParseVariantRef(registry, raw); err == nil {
			t.Errorf("ParseVariantRef(registry parent, %q) succeeded, want an error", raw)
		}
		if ref, err := source.ParseVariantRef(local, raw); err != nil || ref.Kind == source.KindRegistry {
			t.Errorf("ParseVariantRef(local parent, %q) = %+v, %v; want a local ref", raw, ref, err)
		}
	}
	ref, err := source.ParseVariantRef(registry, "ghcr.io/example/support-openrc:v1")
	if err != nil || ref.Kind != source.KindRegistry {
		t.Errorf("ParseVariantRef(registry parent, registry ref) = %+v, %v", ref, err)
	}
}

func TestParseVariantRefRegistryMatch(t *testing.T) {
	registry := source.Ref{Kind: source.KindRegistry, Value: "ghcr.io/example/support:v1"}
	local := source.Ref{Kind: source.KindOCIArchive, Value: "support.tar"}

	// A sibling repository under the same registry host is fine.
	if _, err := source.ParseVariantRef(registry, "ghcr.io/example/support-openrc:v1"); err != nil {
		t.Errorf("same registry, different repository: got error %v, want none", err)
	}

	// A different registry host is rejected, naming both registries.
	_, err := source.ParseVariantRef(registry, "docker.io/example/support-openrc:v1")
	if err == nil {
		t.Fatal("different registry: succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "ghcr.io") || !strings.Contains(err.Error(), "index.docker.io") {
		t.Errorf("different registry: error %q does not name both registries", err.Error())
	}

	// "docker.io" and "index.docker.io" name the same registry.
	dockerParent := source.Ref{Kind: source.KindRegistry, Value: "docker.io/example/support:v1"}
	if _, err := source.ParseVariantRef(dockerParent, "index.docker.io/example/support-openrc:v1"); err != nil {
		t.Errorf("docker.io vs index.docker.io: got error %v, want none (same registry)", err)
	}
	indexParent := source.Ref{Kind: source.KindRegistry, Value: "index.docker.io/example/support:v1"}
	if _, err := source.ParseVariantRef(indexParent, "docker.io/example/support-openrc:v1"); err != nil {
		t.Errorf("index.docker.io vs docker.io: got error %v, want none (same registry)", err)
	}

	// A local parent may still name a variant in any registry.
	if ref, err := source.ParseVariantRef(local, "docker.io/example/support-openrc:v1"); err != nil || ref.Kind != source.KindRegistry {
		t.Errorf("ParseVariantRef(local parent, registry ref) = %+v, %v; want a registry ref, no error", ref, err)
	}
}
