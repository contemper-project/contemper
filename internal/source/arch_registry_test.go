package source_test

import (
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/source"
)

// registryHost starts an in-memory registry and returns its host:port.
func registryHost(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func testImage(t *testing.T, arch string) v1.Image {
	t.Helper()
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: arch}, nil,
		[]imgtest.File{{Path: "marker", Data: []byte(arch)}})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func platformOf(arch, variant string) v1.Descriptor {
	return v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch, Variant: variant}}
}

// checkPlatformsMatchLoad asserts Platforms lists exactly the
// architectures Load can load from ref, and that Load picks the image
// carrying that architecture's marker.
func checkPlatformsMatchLoad(t *testing.T, ref source.Ref, want []string) {
	t.Helper()
	got, err := source.Platforms(t.Context(), ref)
	if err != nil {
		t.Fatalf("Platforms: %v", err)
	}
	var gotArchs []string
	for _, p := range got {
		gotArchs = append(gotArchs, p.Architecture)
	}
	if !reflect.DeepEqual(gotArchs, want) {
		t.Fatalf("Platforms = %v, want %v", gotArchs, want)
	}
	for _, arch := range source.SupportedArchs {
		listed := false
		for _, w := range want {
			listed = listed || w == arch
		}
		img, err := source.Load(t.Context(), ref, v1.Platform{OS: "linux", Architecture: arch})
		if listed != (err == nil) {
			t.Errorf("Load(%s): err = %v, but Platforms listed it = %v", arch, err, listed)
			continue
		}
		if err != nil {
			continue
		}
		cfg, err := img.Image.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Architecture != arch {
			t.Errorf("Load(%s) returned a %s image", arch, cfg.Architecture)
		}
		img.Close()
	}
}

func TestRegistryPlatformsAgreeWithLoad(t *testing.T) {
	host := registryHost(t)
	amd64, arm64 := testImage(t, "amd64"), testImage(t, "arm64")

	cases := []struct {
		name string
		push func(ref name.Reference) error
		want []string
	}{
		{
			name: "oci index",
			want: []string{"amd64", "arm64"},
			push: func(ref name.Reference) error {
				idx := mutate.AppendManifests(empty.Index,
					mutate.IndexAddendum{Add: amd64, Descriptor: platformOf("amd64", "")},
					mutate.IndexAddendum{Add: arm64, Descriptor: platformOf("arm64", "")})
				return remote.WriteIndex(ref, idx)
			},
		},
		{
			name: "docker manifest list",
			want: []string{"amd64", "arm64"},
			push: func(ref name.Reference) error {
				idx := mutate.IndexMediaType(mutate.AppendManifests(empty.Index,
					mutate.IndexAddendum{Add: amd64, Descriptor: platformOf("amd64", "")},
					mutate.IndexAddendum{Add: arm64, Descriptor: platformOf("arm64", "")}), types.DockerManifestList)
				return remote.WriteIndex(ref, idx)
			},
		},
		{
			name: "arm64 variant",
			want: []string{"arm64"},
			push: func(ref name.Reference) error {
				idx := mutate.AppendManifests(empty.Index,
					mutate.IndexAddendum{Add: arm64, Descriptor: platformOf("arm64", "v8")})
				return remote.WriteIndex(ref, idx)
			},
		},
		{
			// An index inside an index, as buildx and containerd
			// produce: the registry client alone would not look inside.
			name: "nested index",
			want: []string{"amd64", "arm64"},
			push: func(ref name.Reference) error {
				inner := mutate.AppendManifests(empty.Index,
					mutate.IndexAddendum{Add: amd64, Descriptor: platformOf("amd64", "")},
					mutate.IndexAddendum{Add: arm64, Descriptor: platformOf("arm64", "v8")})
				outer := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: inner})
				return remote.WriteIndex(ref, outer)
			},
		},
		{
			name: "single manifest",
			want: []string{"arm64"},
			push: func(ref name.Reference) error { return remote.Write(ref, arm64) },
		},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refStr := host + "/test/img" + string(rune('a'+i)) + ":v1"
			ref, err := name.ParseReference(refStr)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.push(ref); err != nil {
				t.Fatalf("pushing: %v", err)
			}
			checkPlatformsMatchLoad(t, source.Ref{Kind: source.KindRegistry, Value: refStr}, c.want)
		})
	}
}

func TestRegistryResolveAll(t *testing.T) {
	host := registryHost(t)
	inner := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: testImage(t, "amd64"), Descriptor: platformOf("amd64", "")},
		mutate.IndexAddendum{Add: testImage(t, "arm64"), Descriptor: platformOf("arm64", "v8")})
	outer := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: inner})
	refStr := host + "/test/app:v1"
	ref, _ := name.ParseReference(refStr)
	if err := remote.WriteIndex(ref, outer); err != nil {
		t.Fatal(err)
	}

	sel, _ := source.ParseArchSelection("all")
	ps, err := sel.Resolve(t.Context(), source.Ref{Kind: source.KindRegistry, Value: refStr})
	if err != nil || len(ps) != 2 {
		t.Fatalf("Resolve(all) = %v, %v", ps, err)
	}
	for _, p := range ps {
		img, err := source.Load(t.Context(), source.Ref{Kind: source.KindRegistry, Value: refStr}, p)
		if err != nil {
			t.Errorf("Load(%s) after Resolve: %v", p.Architecture, err)
			continue
		}
		if img.RepoBase != "app" || img.Tag != "v1" {
			t.Errorf("name = %s-%s, want app-v1", img.RepoBase, img.Tag)
		}
		img.Close()
	}
}

func TestRegistryMissingPlatformFailsInResolve(t *testing.T) {
	host := registryHost(t)
	refStr := host + "/test/solo:v1"
	ref, _ := name.ParseReference(refStr)
	if err := remote.Write(ref, testImage(t, "arm64")); err != nil {
		t.Fatal(err)
	}
	sel, _ := source.ParseArchSelection("amd64,arm64")
	_, err := sel.Resolve(t.Context(), source.Ref{Kind: source.KindRegistry, Value: refStr})
	if err == nil || !strings.Contains(err.Error(), "no linux/amd64 platform") {
		t.Errorf("Resolve error = %v", err)
	}
}
