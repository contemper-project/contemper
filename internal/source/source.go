// Package source resolves a contemper source reference (a registry image,
// an OCI archive, an OCI layout directory, or a docker-archive tarball)
// into a platform-selected v1.Image, and checks the contemper readiness
// label before any layer content is fetched.
package source

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// ReadyLabel is the config label that marks an image as contemper-ready.
const ReadyLabel = "io.contemper.ready"

// RefNameAnnotation is the OCI annotation that records the original
// "name:tag" a layout or archive source was saved from (written by
// tools like `podman save`/`buildah push` under this well-known key).
// Docker's containerd image store instead sets this to a bare tag (see
// ContainerdNameAnnotation), so it is only trusted as a bundle name when
// it looks like "repo:tag" or "repo".
const RefNameAnnotation = "org.opencontainers.image.ref.name"

// ContainerdNameAnnotation is the annotation containerd-backed image
// stores (including Docker with the containerd image store, and `ctr
// images export`) set to the full reference an image was named with,
// e.g. "docker.io/library/example:dev". It is preferred over
// RefNameAnnotation, which those same tools set to just the tag.
const ContainerdNameAnnotation = "io.containerd.image.name"

// maxIndexDepth bounds how many levels of nested image index
// resolveDescriptor will follow before giving up. Real layouts nest at
// most two deep (an index of per-tag indexes, each listing per-platform
// manifests alongside attestation manifests); the limit exists to turn a
// malformed or cyclic layout into an error instead of a long walk.
const maxIndexDepth = 8

// dockerReferenceTypeAnnotation and dockerReferenceTypeAttestation mark
// a descriptor as a buildx/containerd attestation manifest rather than
// an image manifest. Docker's containerd image store and containerd's
// own exporters attach one of these, with platform "unknown/unknown", to
// each attestation alongside the real per-platform manifests in a
// nested index; selectManifest must skip them rather than mistake one
// for a platform's manifest.
const (
	dockerReferenceTypeAnnotation  = "vnd.docker.reference.type"
	dockerReferenceTypeAttestation = "attestation-manifest"
)

// Kind identifies which of the four supported source forms a Ref names.
type Kind string

const (
	KindRegistry      Kind = "registry"
	KindOCIArchive    Kind = "oci-archive"
	KindOCILayout     Kind = "oci"
	KindDockerArchive Kind = "docker-archive"
)

// Ref is a parsed source reference.
type Ref struct {
	Kind Kind
	// Value is the registry reference string for KindRegistry, or a
	// filesystem path (tarball or directory) for the other kinds.
	Value string
}

// ParseRef parses a source reference of the form understood by contemper:
// a plain registry reference, or one of "oci-archive:<path>",
// "oci:<path>", "docker-archive:<path>". There is no daemon source.
func ParseRef(raw string) (Ref, error) {
	for _, prefix := range []Kind{KindOCIArchive, KindOCILayout, KindDockerArchive} {
		p := string(prefix) + ":"
		if strings.HasPrefix(raw, p) {
			value := strings.TrimPrefix(raw, p)
			if value == "" {
				return Ref{}, fmt.Errorf("source ref %q: missing path after %q", raw, p)
			}
			return Ref{Kind: prefix, Value: value}, nil
		}
	}
	if raw == "" {
		return Ref{}, fmt.Errorf("empty source ref")
	}
	return Ref{Kind: KindRegistry, Value: raw}, nil
}

// String returns the reference in the form ParseRef accepts, with local
// paths cleaned (so "a/../b.tar" is recorded as "b.tar").
func (r Ref) String() string {
	if r.Kind == KindRegistry {
		return r.Value
	}
	return string(r.Kind) + ":" + filepath.Clean(r.Value)
}

// HostPlatform returns the platform to resolve a source/support image
// against: linux/<archOverride> if given, otherwise linux/<host GOARCH>.
func HostPlatform(archOverride string) (v1.Platform, error) {
	arch := archOverride
	if arch == "" {
		arch = runtime.GOARCH
	}
	switch arch {
	case "amd64", "arm64":
	default:
		return v1.Platform{}, fmt.Errorf("unsupported --arch %q (want amd64 or arm64)", arch)
	}
	return v1.Platform{OS: "linux", Architecture: arch}, nil
}

// Image is a resolved source image plus the identity information the
// bundle manifest and bundle naming need.
type Image struct {
	Image v1.Image
	// Digest is the resolved manifest digest of Image.
	Digest v1.Hash
	// Ref is the reference this image was resolved from.
	Ref Ref
	// RepoBase and Tag name the bundle directory: <RepoBase>-<Tag>.<arch>.
	RepoBase string
	Tag      string
	// Reproducible is false for sources that cannot be re-fetched
	// byte-for-byte from the ref alone (archive and layout sources).
	Reproducible bool

	cleanup func()
}

// Close releases any temporary resources (e.g. an extracted oci-archive
// directory) created while loading the image.
func (img *Image) Close() {
	if img != nil && img.cleanup != nil {
		img.cleanup()
	}
}

// Load resolves ref to a platform-selected image.
func Load(ref Ref, platform v1.Platform) (*Image, error) {
	switch ref.Kind {
	case KindRegistry:
		return loadRegistry(ref, platform)
	case KindOCIArchive:
		return loadOCIArchive(ref, platform)
	case KindOCILayout:
		return loadOCILayout(ref, platform, archiveBaseName(ref.Value))
	case KindDockerArchive:
		return loadDockerArchive(ref, platform)
	default:
		return nil, fmt.Errorf("unknown source kind %q", ref.Kind)
	}
}

func loadRegistry(ref Ref, platform v1.Platform) (*Image, error) {
	nref, err := name.ParseReference(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("parsing registry ref %q: %w", ref.Value, err)
	}
	img, err := remote.Image(nref,
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(platform))
	if err != nil {
		return nil, fmt.Errorf("pulling %s: %w", ref.Value, err)
	}
	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("reading digest of %s: %w", ref.Value, err)
	}
	base := path.Base(nref.Context().RepositoryStr())
	tag := "latest"
	if t, ok := nref.(name.Tag); ok {
		tag = t.TagStr()
	}
	return &Image{
		Image:        img,
		Digest:       digest,
		Ref:          ref,
		RepoBase:     base,
		Tag:          tag,
		Reproducible: true,
	}, nil
}

func loadOCIArchive(ref Ref, platform v1.Platform) (*Image, error) {
	tmpDir, err := os.MkdirTemp("", "contemper-oci-archive-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	f, err := os.Open(ref.Value)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opening oci-archive %q: %w", ref.Value, err)
	}
	defer f.Close()

	if err := extractTar(f, tmpDir); err != nil {
		cleanup()
		return nil, fmt.Errorf("extracting oci-archive %q: %w", ref.Value, err)
	}

	img, err := loadOCILayout(Ref{Kind: KindOCILayout, Value: tmpDir}, platform, archiveBaseName(ref.Value))
	if err != nil {
		cleanup()
		return nil, err
	}
	img.Ref = ref
	img.cleanup = cleanup
	return img, nil
}

// loadOCILayout resolves ref (an OCI layout directory) to a
// platform-selected image. fallbackBase names the bundle when no naming
// annotation is found on the resolved manifest's lineage of index
// descriptors; callers pass a name derived from the archive/directory
// path they loaded ref from.
func loadOCILayout(ref Ref, platform v1.Platform, fallbackBase string) (*Image, error) {
	idx, err := layout.ImageIndexFromPath(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("reading OCI layout %q: %w", ref.Value, err)
	}

	leaf, leafIndex, topDesc, topDescs, err := resolveDescriptor(idx, platform, ref.Value)
	if err != nil {
		return nil, err
	}

	img, err := leafIndex.Image(leaf.Digest)
	if err != nil {
		return nil, fmt.Errorf("reading image %s from %q: %w", leaf.Digest, ref.Value, err)
	}

	base, tag := bundleName(topDescs, topDesc, fallbackBase)

	return &Image{
		Image:        img,
		Digest:       leaf.Digest,
		Ref:          ref,
		RepoBase:     base,
		Tag:          tag,
		Reproducible: false,
	}, nil
}

func loadDockerArchive(ref Ref, platform v1.Platform) (*Image, error) {
	opener := func() (io.ReadCloser, error) { return os.Open(ref.Value) }
	manifest, err := tarball.LoadManifest(opener)
	if err != nil {
		return nil, fmt.Errorf("reading docker-archive %q: %w", ref.Value, err)
	}
	if len(manifest) == 0 {
		return nil, fmt.Errorf("docker-archive %q has no images", ref.Value)
	}
	img, err := tarball.Image(opener, nil)
	if err != nil {
		return nil, fmt.Errorf("reading docker-archive %q: %w", ref.Value, err)
	}

	// docker-archive is single-platform; if the caller asked for a
	// specific architecture, verify the loaded config matches.
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading config of %q: %w", ref.Value, err)
	}
	if cfg.OS != "" && cfg.OS != platform.OS || cfg.Architecture != "" && cfg.Architecture != platform.Architecture {
		return nil, fmt.Errorf("%q is %s/%s, want %s/%s", ref.Value, cfg.OS, cfg.Architecture, platform.OS, platform.Architecture)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("reading digest of %q: %w", ref.Value, err)
	}

	base, tag := "image", "latest"
	if len(manifest[0].RepoTags) > 0 {
		base, tag = splitRepoTag(manifest[0].RepoTags[0])
	}

	return &Image{
		Image:        img,
		Digest:       digest,
		Ref:          ref,
		RepoBase:     base,
		Tag:          tag,
		Reproducible: false,
	}, nil
}

// IndexAnnotations returns the annotations recorded on the index
// descriptor that selects platform's manifest, for support-image
// resolution's manifest/index-descriptor annotation fallback (see
// docs/reference/support-image-annotations.md). It returns nil, nil (not
// an error) for a reference that doesn't resolve through a multi-platform
// index, since there is then no descriptor-level fallback to read.
func IndexAnnotations(ref Ref, platform v1.Platform) (map[string]string, error) {
	switch ref.Kind {
	case KindRegistry:
		return registryIndexAnnotations(ref, platform)
	case KindOCIArchive:
		return ociArchiveIndexAnnotations(ref, platform)
	case KindOCILayout:
		return ociLayoutIndexAnnotations(ref, platform)
	case KindDockerArchive:
		// docker-archive's manifest.json has no index-descriptor
		// annotation concept.
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown source kind %q", ref.Kind)
	}
}

func registryIndexAnnotations(ref Ref, platform v1.Platform) (map[string]string, error) {
	nref, err := name.ParseReference(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("parsing registry ref %q: %w", ref.Value, err)
	}
	desc, err := remote.Get(nref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", ref.Value, err)
	}
	if !desc.MediaType.IsIndex() {
		return nil, nil
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("reading index %s: %w", ref.Value, err)
	}
	return indexManifestAnnotations(idx, platform, ref.Value)
}

func ociArchiveIndexAnnotations(ref Ref, platform v1.Platform) (map[string]string, error) {
	tmpDir, err := os.MkdirTemp("", "contemper-oci-archive-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	f, err := os.Open(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("opening oci-archive %q: %w", ref.Value, err)
	}
	defer f.Close()

	if err := extractTar(f, tmpDir); err != nil {
		return nil, fmt.Errorf("extracting oci-archive %q: %w", ref.Value, err)
	}
	return ociLayoutIndexAnnotations(Ref{Kind: KindOCILayout, Value: tmpDir}, platform)
}

func ociLayoutIndexAnnotations(ref Ref, platform v1.Platform) (map[string]string, error) {
	idx, err := layout.ImageIndexFromPath(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("reading OCI layout %q: %w", ref.Value, err)
	}
	return indexManifestAnnotations(idx, platform, ref.Value)
}

// indexManifestAnnotations returns the annotations on the leaf manifest
// descriptor selected for platform, recursing through nested indexes
// (see resolveDescriptor) to reach it. This is the descriptor the
// support-image annotation fallback in docs/reference/support-image-
// annotations.md means by "that platform's descriptor inside the
// index" - the one carrying Platform, however deeply it is nested -
// which is not necessarily the outermost one used for bundle naming.
func indexManifestAnnotations(idx v1.ImageIndex, platform v1.Platform, name string) (map[string]string, error) {
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("reading index manifest of %q: %w", name, err)
	}
	if len(im.Manifests) == 0 {
		return nil, nil
	}
	leaf, _, _, _, err := resolveDescriptor(idx, platform, name)
	if err != nil {
		return nil, err
	}
	return leaf.Annotations, nil
}

// resolveDescriptor walks idx, and any image index it points to in turn
// (up to maxIndexDepth levels), applying selectManifest at each level -
// so a containerd-produced layout, where index.json points to a second
// index before reaching the per-platform manifests and attestation
// manifests selectManifest ignores, still resolves.
//
// It returns the leaf descriptor (never itself an index, since
// selectManifest's result is followed into ImageIndex() until it isn't)
// together with the v1.ImageIndex it was found in, so a caller can fetch
// leafIndex.Image(leaf.Digest); and separately the outermost index's
// selected descriptor, topDesc, and all of that index's sibling
// descriptors, topDescs - since tools that write these layouts set
// image-naming annotations at the outermost level regardless of how
// deeply the actual platform manifests end up nested.
func resolveDescriptor(idx v1.ImageIndex, platform v1.Platform, name string) (leaf v1.Descriptor, leafIndex v1.ImageIndex, topDesc v1.Descriptor, topDescs []v1.Descriptor, err error) {
	cur := idx
	for depth := 0; ; depth++ {
		if depth > maxIndexDepth {
			return v1.Descriptor{}, nil, v1.Descriptor{}, nil, fmt.Errorf("%q: image index nesting exceeds %d levels", name, maxIndexDepth)
		}
		im, err := cur.IndexManifest()
		if err != nil {
			return v1.Descriptor{}, nil, v1.Descriptor{}, nil, fmt.Errorf("reading index manifest of %q: %w", name, err)
		}
		if len(im.Manifests) == 0 {
			return v1.Descriptor{}, nil, v1.Descriptor{}, nil, fmt.Errorf("%q has no manifests", name)
		}

		desc, err := selectManifest(im.Manifests, platform)
		if err != nil {
			return v1.Descriptor{}, nil, v1.Descriptor{}, nil, fmt.Errorf("%q: %w", name, err)
		}
		if depth == 0 {
			topDesc, topDescs = desc, im.Manifests
		}
		if !desc.MediaType.IsIndex() {
			return desc, cur, topDesc, topDescs, nil
		}
		cur, err = cur.ImageIndex(desc.Digest)
		if err != nil {
			return v1.Descriptor{}, nil, v1.Descriptor{}, nil, fmt.Errorf("reading nested index %s of %q: %w", desc.Digest, name, err)
		}
	}
}

// selectManifest picks the descriptor matching platform from an index's
// manifest list, ignoring attestation manifests. A single-manifest
// layout with no platform metadata at all (a single-arch archive, or an
// outer descriptor that wraps a nested index) is accepted
// unconditionally. The returned descriptor may itself be an index;
// resolveDescriptor is what recurses into one to reach a leaf manifest.
func selectManifest(descs []v1.Descriptor, platform v1.Platform) (v1.Descriptor, error) {
	var candidates []v1.Descriptor
	for _, d := range descs {
		if !isAttestationManifest(d) {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) == 0 {
		// Every descriptor looked like an attestation manifest (or the
		// list was empty to begin with); fall back to the unfiltered
		// list so platform matching below still runs and fails with its
		// usual error rather than a confusing "no manifests" here.
		candidates = descs
	}

	var manifestDescs []v1.Descriptor
	for _, d := range candidates {
		if d.MediaType.IsImage() || d.MediaType.IsIndex() {
			manifestDescs = append(manifestDescs, d)
		}
	}
	if len(manifestDescs) == 0 {
		manifestDescs = candidates
	}
	if len(manifestDescs) == 1 && manifestDescs[0].Platform == nil {
		return manifestDescs[0], nil
	}
	for _, d := range manifestDescs {
		if d.Platform == nil {
			continue
		}
		if d.Platform.OS == platform.OS && d.Platform.Architecture == platform.Architecture {
			return d, nil
		}
	}
	return v1.Descriptor{}, fmt.Errorf("no manifest for platform %s/%s", platform.OS, platform.Architecture)
}

// isAttestationManifest reports whether d is a buildx/containerd
// attestation manifest rather than an image: it carries the
// "attestation-manifest" reference-type annotation, or the placeholder
// platform "unknown/unknown" those manifests are published under (or
// both).
func isAttestationManifest(d v1.Descriptor) bool {
	if d.Annotations[dockerReferenceTypeAnnotation] == dockerReferenceTypeAttestation {
		return true
	}
	return d.Platform != nil && d.Platform.OS == "unknown" && d.Platform.Architecture == "unknown"
}

// bundleName derives (base, tag) for naming the bundle directory from
// the naming annotations tools set on an index descriptor: it prefers
// ContainerdNameAnnotation, always a full reference, over
// RefNameAnnotation, which Docker's containerd image store (and other
// containerd-backed tools) sets to a bare tag when the full reference is
// already in ContainerdNameAnnotation. It checks selected's own
// annotations first, then every descriptor in descs, since some tools
// only set the annotation on one entry of an otherwise-unannotated
// list. fallbackBase names the bundle when neither annotation is found
// anywhere in descs.
func bundleName(descs []v1.Descriptor, selected v1.Descriptor, fallbackBase string) (base, tag string) {
	if name := annotationOf(descs, selected, ContainerdNameAnnotation); name != "" {
		return splitRepoTag(name)
	}
	if name := annotationOf(descs, selected, RefNameAnnotation); name != "" {
		if !strings.ContainsAny(name, "/:") {
			// A bare tag, not "repo:tag" or "repo": Docker's containerd
			// image store sets RefNameAnnotation this way when it has
			// already recorded the full reference under
			// ContainerdNameAnnotation, so treat it as the tag alone
			// rather than as a repo name with an implied "latest" tag.
			return fallbackBase, name
		}
		return splitRepoTag(name)
	}
	return fallbackBase, "latest"
}

func annotationOf(descs []v1.Descriptor, selected v1.Descriptor, key string) string {
	if v, ok := selected.Annotations[key]; ok {
		return v
	}
	for _, d := range descs {
		if v, ok := d.Annotations[key]; ok {
			return v
		}
	}
	return ""
}

// archiveBaseName derives a fallback bundle-name base from the path of
// an archive or layout directory a source was loaded from, for the
// (Docker/buildx-produced) sources that carry no naming annotation at
// all: "a/b/example.tar" and "a/b/example.tar.gz" both give "example",
// as does the layout directory "a/b/example".
func archiveBaseName(p string) string {
	base := filepath.Base(filepath.Clean(p))
	for _, ext := range []string{".tar.gz", ".tar.zst", ".tgz", ".tar"} {
		if trimmed := strings.TrimSuffix(base, ext); trimmed != base {
			return trimmed
		}
	}
	return base
}

// splitRepoTag splits a "repo:tag" or "repo" string into a bundle-naming
// (basename, tag) pair.
func splitRepoTag(s string) (string, string) {
	repo, tag := s, "latest"
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "/") {
		repo, tag = s[:i], s[i+1:]
	}
	return path.Base(repo), tag
}

// extractTar extracts a tar stream into dir. It is used only for OCI
// archives, whose contents are content-addressed blobs and an index -
// not attributable file content from an authored image - so this does
// not run afoul of the "never extract image contents" rule.
func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.Clean(string(filepath.Separator)+hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		default:
			// OCI layouts contain only dirs and regular files.
		}
	}
}

// CheckReady fails unless cfg carries the contemper readiness label.
func CheckReady(cfg *v1.ConfigFile) error {
	if cfg == nil || cfg.Config.Labels[ReadyLabel] != "true" {
		return fmt.Errorf("image is not contemper-ready: label %s=\"true\" is required", ReadyLabel)
	}
	return nil
}
