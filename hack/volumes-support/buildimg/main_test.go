package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuildIndexAnnotations checks that buildIndex sets the OCI
// annotations on both the index itself and each platform manifest,
// while leaving the per-descriptor annotations (the ones contemper's
// own reader looks at, docs/reference/support-image-annotations.md)
// exactly as given.
func TestBuildIndexAnnotations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	descAnnotations := map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files": "/sbin/openrc",
	}
	ociAnns := map[string]string{
		"org.opencontainers.image.title":       "volumes-support",
		"org.opencontainers.image.description": "a support image",
		"org.opencontainers.image.source":      "https://github.com/contemper-project/contemper",
		"org.opencontainers.image.licenses":    "Apache-2.0",
	}

	idx, err := buildIndex(dir, descAnnotations, ociAnns)
	if err != nil {
		t.Fatal(err)
	}

	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}

	for k, want := range ociAnns {
		if got := im.Annotations[k]; got != want {
			t.Errorf("index annotation %q = %q, want %q", k, got, want)
		}
	}

	if len(im.Manifests) != len(platforms) {
		t.Fatalf("got %d manifests, want %d", len(im.Manifests), len(platforms))
	}
	for _, desc := range im.Manifests {
		for k, want := range descAnnotations {
			if got := desc.Annotations[k]; got != want {
				t.Errorf("descriptor annotation %q = %q, want %q", k, got, want)
			}
		}
		// The OCI keys must not have leaked into the per-descriptor
		// annotations - they belong on the index and on the platform
		// manifest itself, not here.
		if _, ok := desc.Annotations["org.opencontainers.image.description"]; ok {
			t.Errorf("descriptor annotations unexpectedly carry org.opencontainers.image.description: %v", desc.Annotations)
		}

		img, err := idx.Image(desc.Digest)
		if err != nil {
			t.Fatalf("fetching platform image %s: %v", desc.Digest, err)
		}
		manifest, err := img.Manifest()
		if err != nil {
			t.Fatalf("reading manifest for %s: %v", desc.Digest, err)
		}
		for k, want := range ociAnns {
			if got := manifest.Annotations[k]; got != want {
				t.Errorf("platform manifest annotation %q = %q, want %q", k, got, want)
			}
		}
	}
}

// TestBuildIndexNoOCIAnnotations checks that an empty (or nil) OCI
// annotation map - as buildAndPush passes it for the two variant images
// - leaves index and manifest annotations untouched rather than adding
// an empty org.opencontainers.* set.
func TestBuildIndexNoOCIAnnotations(t *testing.T) {
	dir := t.TempDir()

	idx, err := buildIndex(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(im.Annotations) != 0 {
		t.Errorf("expected no index annotations, got %v", im.Annotations)
	}
	for _, desc := range im.Manifests {
		img, err := idx.Image(desc.Digest)
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := img.Manifest()
		if err != nil {
			t.Fatal(err)
		}
		if len(manifest.Annotations) != 0 {
			t.Errorf("expected no manifest annotations, got %v", manifest.Annotations)
		}
	}
}

// TestOCIAnnotationsRevisionOptional checks that -revision's absence
// (the local/e2e case) simply omits the revision key rather than
// setting it to "", and that it's included when given.
func TestOCIAnnotationsRevisionOptional(t *testing.T) {
	anns := ociAnnotations("volumes-support", "", "a description")
	if _, ok := anns["org.opencontainers.image.revision"]; ok {
		t.Errorf("expected no revision annotation when revision is empty, got %v", anns)
	}

	anns = ociAnnotations("volumes-support", "deadbeef", "a description")
	if anns["org.opencontainers.image.revision"] != "deadbeef" {
		t.Errorf("revision annotation = %q, want %q", anns["org.opencontainers.image.revision"], "deadbeef")
	}
}
