// Package source resolves a contemper source reference (a registry image,
// an OCI archive, an OCI layout directory, a docker-archive tarball, or an
// image already loaded into the local Docker daemon) into a
// platform-selected v1.Image, and checks the contemper readiness label
// before any layer content is fetched.
package source

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/contemper-project/contemper/internal/limits"
	"github.com/contemper-project/contemper/internal/subprocess"
)

// ReadyLabel is the config label that marks an image as contemper-ready.
const ReadyLabel = "io.contemper.ready"

// BootLabel is the config label that selects how the image boots: as a
// UKI contemper assembles, or from the image's own bootloader.
const BootLabel = "io.contemper.boot"

// SecureBootLabel is the config label that asks for the VM to boot with
// UEFI Secure Boot enabled: "true" or "false" (the default).
const SecureBootLabel = "io.contemper.secure-boot"

// Boot modes BootLabel can select, also the values recorded in the bundle
// manifest.
const (
	BootUKI        = "uki"
	BootBootloader = "bootloader"
)

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

// Kind identifies which of the five supported source forms a Ref names.
type Kind string

// The five supported Kind values, one per source form Ref.Kind can name.
const (
	KindRegistry      Kind = "registry"
	KindOCIArchive    Kind = "oci-archive"
	KindOCILayout     Kind = "oci"
	KindDockerArchive Kind = "docker-archive"
	// KindDockerDaemon names an image already loaded into the local
	// Docker daemon's image store, read out with `docker save` (skopeo's
	// name for this source form).
	KindDockerDaemon Kind = "docker-daemon"
)

// Ref is a parsed source reference.
type Ref struct {
	Kind Kind
	// Value is the registry reference string for KindRegistry, the
	// image reference to `docker save` for KindDockerDaemon, or a
	// filesystem path (tarball or directory) for the other kinds.
	Value string
	// Anonymous makes every registry request for this reference go
	// out without the user's credentials. ParseVariantRef sets it for
	// variants outside their support image's namespace. Ignored for
	// the other kinds.
	Anonymous bool
}

// ParseRef parses a source reference of the form understood by contemper:
// a plain registry reference, or one of "oci-archive:<path>",
// "oci:<path>", "docker-archive:<path>", "docker-daemon:<ref>".
func ParseRef(raw string) (Ref, error) {
	for _, prefix := range []Kind{KindOCIArchive, KindOCILayout, KindDockerArchive, KindDockerDaemon} {
		p := string(prefix) + ":"
		if strings.HasPrefix(raw, p) {
			value := strings.TrimPrefix(raw, p)
			if value == "" {
				what := "path"
				if prefix == KindDockerDaemon {
					what = "image reference"
				}
				return Ref{}, fmt.Errorf("source ref %q: missing %s after %q", raw, what, p)
			}
			if prefix == KindDockerDaemon && strings.HasPrefix(value, "-") {
				// The value becomes an argument to `docker save`; one
				// starting with "-" would be read as a flag there.
				return Ref{}, fmt.Errorf("source ref %q: image reference must not start with \"-\"", raw)
			}
			return Ref{Kind: prefix, Value: value}, nil
		}
	}
	if raw == "" {
		return Ref{}, fmt.Errorf("empty source ref")
	}
	return Ref{Kind: KindRegistry, Value: raw}, nil
}

// ParseVariantRef parses raw, a variant image reference read from the
// labels of the image parent was loaded from. Labels are image content:
// a support image pulled from a registry must not be able to point
// contemper at files on the build host, so when parent is a registry
// reference, raw must be a registry reference too. A local parent (an
// archive or layout the user supplied) may name local variants, or any
// registry, and those are pulled with the user's credentials since the
// user already chose to trust that parent image directly.
//
// A registry parent constrains how a variant is pulled. A variant in the
// same registry and repository namespace (the first path component, as
// in "ghcr.io/acme/" for "ghcr.io/acme/support"; the repository itself
// may differ, since the published variants of a real support image live
// in sibling repositories) is pulled with the user's credentials, so a
// private support image can have private variants. For a support image
// in a repository with no namespace (one path component), only that
// same repository counts. A variant anywhere else (another registry or
// namespace) is allowed but returned with Anonymous set: every request
// for it goes out without credentials. Labels are content the image
// controls, and without credentials they cannot make contemper pull a
// private image the user has access to into the disk. Repository paths
// with ".", ".." or empty segments are refused. A variant in a different
// registry than a registry parent is also refused when that registry is
// a local or private address (see localRegistry): otherwise a published
// support image could make contemper send requests to services on the
// build host's network. Same-registry variants are not affected.
func ParseVariantRef(parent Ref, raw string) (Ref, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return Ref{}, err
	}
	if parent.Kind != KindRegistry {
		return ref, nil
	}
	if ref.Kind != KindRegistry {
		return Ref{}, fmt.Errorf("variant image %q is a local %s reference, but the image declaring it came from a registry; only registry references are allowed there", raw, ref.Kind)
	}
	parentRegistry, parentRepo, err := registryRepository(parent.Value)
	if err != nil {
		return Ref{}, fmt.Errorf("parsing support image ref %q: %w", parent.Value, err)
	}
	variantRegistry, variantRepo, err := registryRepository(ref.Value)
	if err != nil {
		return Ref{}, fmt.Errorf("parsing variant image %q: %w", raw, err)
	}
	parentNS := repoNamespace(parentRepo)
	switch {
	case variantRegistry != parentRegistry:
		if localRegistry(variantRegistry) {
			return Ref{}, fmt.Errorf("variant image %q is on %s, a local or private address; a support image from %s may not name variants there", raw, variantRegistry, parentRegistry)
		}
		ref.Anonymous = true
	case parentNS == "":
		// A repository with no namespace: nothing groups it with others,
		// so only that same repository is trusted with credentials.
		ref.Anonymous = variantRepo != parentRepo
	default:
		ref.Anonymous = repoNamespace(variantRepo) != parentNS
	}
	return ref, nil
}

// localRegistry reports whether host (a registry host, with an optional
// port) is one go-containerregistry would contact over plain HTTP
// (localhost, loopback, ".local" names, private ranges), or an IP
// literal that is loopback, link-local, private or unspecified. A support
// image from another registry must not be able to make contemper send
// requests to services on the build host's network by naming such a
// variant.
func localRegistry(host string) bool {
	reg, err := name.NewRegistry(host)
	if err != nil || reg.Scheme() == "http" {
		return true
	}
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsPrivate() || ip.IsUnspecified()
	}
	return false
}

// registryRepository returns the normalized registry host of a registry
// reference string and its repository path. It refuses a repository path
// that is not already clean (".", ".." or empty segments): registries and
// the HTTP layers in front of them may resolve such a path to another
// repository than the one a namespace comparison would see.
func registryRepository(raw string) (host, repo string, err error) {
	nref, err := name.ParseReference(raw)
	if err != nil {
		return "", "", err
	}
	repo = nref.Context().RepositoryStr()
	if path.Clean(repo) != repo || strings.HasPrefix(repo, "../") || repo == ".." {
		return "", "", fmt.Errorf("repository path %q must not contain \".\", \"..\" or empty segments", repo)
	}
	return nref.Context().RegistryStr(), repo, nil
}

// repoNamespace returns the first component of a repository path (the
// owner or organization on most registries; "library" for a single-name
// Docker Hub image), or "" if the path has only one component.
func repoNamespace(repo string) string {
	ns, _, _ := strings.Cut(repo, "/")
	if ns == repo {
		return ""
	}
	return ns
}

// String returns the reference in the form ParseRef accepts, with local
// paths cleaned (so "a/../b.tar" is recorded as "b.tar"). A
// KindDockerDaemon Value is a Docker image reference, not a path, so it
// is left as given.
func (r Ref) String() string {
	switch r.Kind {
	case KindRegistry:
		return r.Value
	case KindDockerDaemon:
		return string(r.Kind) + ":" + r.Value
	default:
		return string(r.Kind) + ":" + filepath.Clean(r.Value)
	}
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

// Load resolves ref to a platform-selected image. Canceling ctx stops a
// registry pull or `docker save` (KindDockerDaemon) in progress.
func Load(ctx context.Context, ref Ref, platform v1.Platform) (*Image, error) {
	switch ref.Kind {
	case KindRegistry:
		return loadRegistry(ctx, ref, platform)
	case KindOCIArchive:
		return loadOCIArchive(ctx, ref, platform)
	case KindOCILayout:
		return loadOCILayout(ref, platform, archiveBaseName(ref.Value))
	case KindDockerArchive:
		return loadDockerArchive(ref, platform)
	case KindDockerDaemon:
		return loadDockerDaemon(ctx, ref, platform)
	default:
		return nil, fmt.Errorf("unknown source kind %q", ref.Kind)
	}
}

// registryOptions are the remote options every registry read uses, so
// listing a source's platforms and loading one see the same registry.
func registryOptions(ctx context.Context, anonymous bool) []remote.Option {
	auth := remote.WithAuthFromKeychain(authn.DefaultKeychain)
	if anonymous {
		auth = remote.WithAuth(authn.Anonymous)
	}
	return []remote.Option{remote.WithContext(ctx), auth}
}

// anonymousPullError adds to err, a failed registry request for ref, a
// note that the request carried no credentials when ref is anonymous and
// the registry answered with an authentication or authorization error
// (some registries answer 404 for a private repository).
func anonymousPullError(ref Ref, err error) error {
	var terr *transport.Error
	if !ref.Anonymous || !errors.As(err, &terr) {
		return err
	}
	switch terr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%w (the variant image is outside its support image's namespace and was fetched without credentials)", err)
	}
	return err
}

// loadRegistry resolves a registry ref the way a layout is resolved: an
// index (OCI or Docker manifest list, nested or not) goes through
// resolveDescriptor, the same walk Platforms uses, and a plain manifest
// is the image itself.
func loadRegistry(ctx context.Context, ref Ref, platform v1.Platform) (*Image, error) {
	nref, err := name.ParseReference(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("parsing registry ref %q: %w", ref.Value, err)
	}
	desc, err := remote.Get(nref, registryOptions(ctx, ref.Anonymous)...)
	if err != nil {
		return nil, fmt.Errorf("pulling %s: %w", ref.Value, anonymousPullError(ref, err))
	}
	var img v1.Image
	var digest v1.Hash
	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("reading index %s: %w", ref.Value, err)
		}
		leaf, leafIndex, _, _, err := resolveDescriptor(idx, platform, ref.Value)
		if err != nil {
			return nil, fmt.Errorf("pulling %s: %w", ref.Value, err)
		}
		img, err = leafIndex.Image(leaf.Digest)
		if err != nil {
			return nil, fmt.Errorf("pulling %s: %w", ref.Value, err)
		}
		digest = leaf.Digest
	} else {
		img, err = desc.Image()
		if err != nil {
			return nil, fmt.Errorf("pulling %s: %w", ref.Value, err)
		}
		if err := checkImagePlatform(img, ref.Value, platform); err != nil {
			return nil, err
		}
		digest, err = img.Digest()
		if err != nil {
			return nil, fmt.Errorf("reading digest of %s: %w", ref.Value, err)
		}
	}
	base, tag := referenceName(nref)
	return &Image{
		Image:        img,
		Digest:       digest,
		Ref:          ref,
		RepoBase:     base,
		Tag:          tag,
		Reproducible: true,
	}, nil
}

func loadOCIArchive(ctx context.Context, ref Ref, platform v1.Platform) (*Image, error) {
	tmpDir, err := os.MkdirTemp("", "contemper-oci-archive-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	f, err := os.Open(ref.Value)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opening oci-archive %q: %w", ref.Value, err)
	}
	defer func() { _ = f.Close() }()

	if err := extractTar(ctx, f, tmpDir); err != nil {
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

// checkImagePlatform verifies that a single image, one with no platform
// on a descriptor to select it by, is for platform according to its own
// config. A config that leaves the OS or architecture empty matches
// anything, as Platforms reports.
func checkImagePlatform(img v1.Image, name string, platform v1.Platform) error {
	cfg, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("reading config of %q: %w", name, err)
	}
	if cfg.OS != "" && cfg.OS != platform.OS || cfg.Architecture != "" && cfg.Architecture != platform.Architecture {
		return fmt.Errorf("%q is %s/%s, want %s/%s", name, cfg.OS, cfg.Architecture, platform.OS, platform.Architecture)
	}
	return nil
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
	if leaf.Platform == nil {
		// Selected only because it is the sole image: its own config
		// must agree with the request, as Platforms reports it.
		if err := checkImagePlatform(img, ref.Value, platform); err != nil {
			return nil, err
		}
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
	if err := checkImagePlatform(img, ref.Value, platform); err != nil {
		return nil, err
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

// loadDockerDaemon resolves ref (a "docker-daemon:<ref>" source, ref.Value
// being the image reference as the Docker daemon knows it) by running
// `docker save` into a temporary archive and reading that archive back
// through the existing archive-loading code, exactly as if the user had
// run `docker save` themselves and pointed contemper at the result.
//
// Docker 25+ writes an OCI layout tarball (index.json, oci-layout,
// blobs/); older Docker (and the classic image store) writes a
// docker-archive tarball (manifest.json, repositories, per-layer
// directories). Which one `docker save` produced is detected by content
// (an "index.json" entry at the archive's root), not by asking Docker's
// version, since both the daemon version and its configured image store
// affect the output format.
//
// The bundle is named after ref.Value itself - the reference the caller
// asked `docker save` for - rather than any naming annotation the
// archive happens to carry, so `docker-daemon:my-app:dev` always yields
// a bundle named after "my-app:dev" regardless of image-store quirks.
//
// The returned Image's cleanup removes the temporary archive (and, for
// the OCI-layout case, the directory it was extracted into); the caller
// must call Close once the image's layers have been read.
//
// Canceling ctx stops the `docker save` subprocess (see
// internal/subprocess).
func loadDockerDaemon(ctx context.Context, ref Ref, platform v1.Platform) (*Image, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker not found on PATH; install Docker to use a docker-daemon: source (https://docs.docker.com/get-docker/)")
	}

	tmpDir, err := os.MkdirTemp("", "contemper-docker-daemon-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	archivePath := filepath.Join(tmpDir, "image.tar")
	// ref.Value is the user-given source ref, never a shell.
	cmd := subprocess.Command(ctx, dockerPath, "save", "-o", archivePath, ref.Value)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return nil, fmt.Errorf("docker save %s: %w", ref.Value, err)
	}

	isOCILayout, err := archiveHasIndexJSON(archivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("reading docker save output for %q: %w", ref.Value, err)
	}

	var img *Image
	if isOCILayout {
		layoutDir := filepath.Join(tmpDir, "layout")
		if err := os.MkdirAll(layoutDir, 0o750); err != nil {
			cleanup()
			return nil, fmt.Errorf("creating layout dir: %w", err)
		}
		f, err := os.Open(archivePath)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("opening %q: %w", archivePath, err)
		}
		extractErr := extractTar(ctx, f, layoutDir)
		_ = f.Close()
		if extractErr != nil {
			cleanup()
			return nil, fmt.Errorf("extracting docker save output for %q: %w", ref.Value, extractErr)
		}
		// Only the extracted layout is read from here on; drop the
		// archive now rather than keep two copies of the image on disk
		// for the whole conversion.
		_ = os.Remove(archivePath)
		img, err = loadOCILayout(Ref{Kind: KindOCILayout, Value: layoutDir}, platform, "image")
		if err != nil {
			cleanup()
			return nil, err
		}
	} else {
		img, err = loadDockerArchive(Ref{Kind: KindDockerArchive, Value: archivePath}, platform)
		if err != nil {
			cleanup()
			return nil, err
		}
	}

	img.Ref = ref
	img.RepoBase, img.Tag = daemonRefName(ref.Value)
	img.Reproducible = false
	img.cleanup = cleanup
	return img, nil
}

// daemonRefName derives the bundle-naming (basename, tag) pair for a
// docker-daemon: reference the same way a registry reference is named
// (see referenceName). A value that doesn't parse as an image reference
// falls back to splitRepoTag.
func daemonRefName(value string) (string, string) {
	nref, err := name.ParseReference(value)
	if err != nil {
		return splitRepoTag(value)
	}
	return referenceName(nref)
}

// referenceName returns the bundle-naming (basename, tag) pair for an
// image reference: the repository's last path component, and its tag,
// or "latest" when it has none or is pinned by digest.
func referenceName(nref name.Reference) (string, string) {
	tag := "latest"
	if t, ok := nref.(name.Tag); ok {
		tag = t.TagStr()
	}
	return path.Base(nref.Context().RepositoryStr()), tag
}

// archiveHasIndexJSON reports whether the tar file at path has an
// "index.json" entry at its root, the marker of an OCI layout tarball
// (as opposed to a legacy docker-archive tarball's "manifest.json").
func archiveHasIndexJSON(archivePath string) (bool, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name == "index.json" {
			return true, nil
		}
	}
}

// IndexAnnotations returns the annotations recorded on the index
// descriptor that selects platform's manifest, for support-image
// resolution's manifest/index-descriptor annotation fallback (see
// docs/reference/support-image-labels.md). It returns nil, nil (not
// an error) for a reference that doesn't resolve through a multi-platform
// index, since there is then no descriptor-level fallback to read.
func IndexAnnotations(ctx context.Context, ref Ref, platform v1.Platform) (map[string]string, error) {
	switch ref.Kind {
	case KindRegistry:
		return registryIndexAnnotations(ctx, ref, platform)
	case KindOCIArchive:
		return ociArchiveIndexAnnotations(ctx, ref, platform)
	case KindOCILayout:
		return ociLayoutIndexAnnotations(ref, platform)
	case KindDockerArchive:
		// docker-archive's manifest.json has no index-descriptor
		// annotation concept.
		return nil, nil
	case KindDockerDaemon:
		// A user can pass --support docker-daemon:<ref> directly. Reading
		// its index-descriptor annotations would mean running `docker
		// save` a second time (Load already ran it once); the manifest
		// annotations support.Parse reads from the loaded image cover the
		// same ground, so this returns no extra fallback rather than
		// paying for another docker save.
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown source kind %q", ref.Kind)
	}
}

func registryIndexAnnotations(ctx context.Context, ref Ref, platform v1.Platform) (map[string]string, error) {
	nref, err := name.ParseReference(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("parsing registry ref %q: %w", ref.Value, err)
	}
	desc, err := remote.Get(nref, registryOptions(ctx, ref.Anonymous)...)
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

func ociArchiveIndexAnnotations(ctx context.Context, ref Ref, platform v1.Platform) (map[string]string, error) {
	tmpDir, err := os.MkdirTemp("", "contemper-oci-archive-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	f, err := os.Open(ref.Value)
	if err != nil {
		return nil, fmt.Errorf("opening oci-archive %q: %w", ref.Value, err)
	}
	defer func() { _ = f.Close() }()

	if err := extractTar(ctx, f, tmpDir); err != nil {
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
// as does the layout directory "a/b/example". A file named just ".tar"
// keeps its name rather than shrinking to nothing.
func archiveBaseName(p string) string {
	base := filepath.Base(filepath.Clean(p))
	for _, ext := range []string{".tar.gz", ".tar.zst", ".tgz", ".tar"} {
		if trimmed := strings.TrimSuffix(base, ext); trimmed != base && trimmed != "" {
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
// not run afoul of the "never extract image contents" rule. Canceling
// ctx is noticed between entries, without waiting for the whole archive
// to extract.
func extractTar(ctx context.Context, r io.Reader, dir string) error {
	return extractTarLimited(ctx, r, dir, 0)
}

// maxMetadataBlob bounds the size of a tar entry extractTarLimited keeps
// when listing an archive's platforms: index.json and manifest, index
// and config blobs are far smaller, layers usually are not.
const maxMetadataBlob = 4 << 20

// extractTarLimited is extractTar that, when maxSize is positive, skips
// regular files larger than maxSize (reading past them without writing).
//
// Whatever maxSize is, the extraction as a whole is bounded by the limits
// in ctx (see package limits): each header is checked before any of its
// content is written, so an entry that claims a huge size fails at once.
func extractTarLimited(ctx context.Context, r io.Reader, dir string, maxSize int64) error {
	tr := tar.NewReader(r)
	count := limits.NewCounter(limits.FromContext(ctx))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := count.Check(hdr.Name, hdr.Size); err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.Clean(string(filepath.Separator)+hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			// dir is always a private temp dir this function's caller
			// just created, so there is no reason for its contents to be
			// group/world readable.
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if maxSize > 0 && hdr.Size > maxSize {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			// Same private-scratch reasoning as the MkdirAll above.
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return err
			}
			// Not a decompression-bomb risk: these are OCI-archive blobs
			// from a local archive the caller chose to convert, sized by
			// their own real content, not an attacker-controlled stream.
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // G110: bounded by the source archive's real content, not attacker-controlled
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
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

// BootMode returns the boot mode cfg selects through BootLabel: BootUKI
// when the label is absent, an error for any value other than the two
// documented ones.
func BootMode(cfg *v1.ConfigFile) (string, error) {
	if cfg == nil {
		return BootUKI, nil
	}
	v, ok := cfg.Config.Labels[BootLabel]
	switch {
	case !ok, v == BootUKI:
		return BootUKI, nil
	case v == BootBootloader:
		return BootBootloader, nil
	}
	return "", fmt.Errorf("label %s has the unknown value %q: use %q or %q", BootLabel, v, BootUKI, BootBootloader)
}

// SecureBoot returns whether cfg asks for UEFI Secure Boot through
// SecureBootLabel: false when the label is absent or "false", an error
// for any value other than "true" and "false", and an error for "true"
// in any boot mode but BootBootloader, since contemper's UKI is
// unsigned and cannot boot under Secure Boot.
func SecureBoot(cfg *v1.ConfigFile, bootMode string) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	v, ok := cfg.Config.Labels[SecureBootLabel]
	switch {
	case !ok, v == "false":
		return false, nil
	case v == "true":
		if bootMode != BootBootloader {
			return false, fmt.Errorf("label %s=\"true\" needs a bootloader image (%s=%q): contemper's UKI is unsigned, so it cannot boot with Secure Boot enabled", SecureBootLabel, BootLabel, BootBootloader)
		}
		return true, nil
	}
	return false, fmt.Errorf("label %s has the unknown value %q: use \"true\" or \"false\"", SecureBootLabel, v)
}
