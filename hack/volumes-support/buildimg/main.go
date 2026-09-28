// Command buildimg builds and pushes the volume-formatting support
// image and its two init-system variant images
// (support/volumes-support/{base,openrc,systemd}) as multi-platform
// (linux/amd64, linux/arm64) indexes, for a given registry prefix and
// tag - the content is architecture-independent (a POSIX sh script, an
// OpenRC init script, a systemd unit and its enablement symlink), so
// each platform's image differs only in its config's declared
// architecture, not its files.
//
// It sets the base image's branch/variant annotations
// (docs/reference/support-image-annotations.md) with each variant
// image's reference pinned BY DIGEST (the variant index's digest, which
// resolves to the right platform on read, the same as any other
// multi-platform reference), never a floating tag, so the support image
// always names an exact, reproducible set of variant bytes.
//
// It also sets the standard org.opencontainers.image.* annotations
// (title, description, source, licenses, and revision when -revision
// is given) on each index and on each platform manifest, so a
// registry UI has something to show for the image. These live in a
// separate namespace from contemper's own io.contemper.* keys above and
// are never read back by contemper (internal/support.Parse only
// recognizes io.contemper.* keys and ignores everything else).
//
// Used both by .github/workflows/volumes-support.yml (publishing to
// ghcr.io/contemper-project) and by hack/e2e-volumes.sh (publishing to a
// local registry for the boot test), for the same reason
// hack/e2e-variants/buildimg exists: building the image directly with
// go-containerregistry - the library contemper itself uses to read it
// back - sidesteps engine-specific ways of setting manifest annotations.
//
// Usage:
//
//	buildimg -prefix ghcr.io/contemper-project -tag v1 \
//	    -support-dir /path/to/support/volumes-support [-revision <commit-sha>] \
//	    [-digests-file /path/to/digests.txt]
package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/contemper-project/contemper/internal/imgtest"
)

// platforms is every platform each of the three images is published
// for, in the fixed order they're appended to each index.
var platforms = []string{"amd64", "arm64"}

// sourceAnnotation and licenseAnnotation are the same for all three
// images: they all live in this one repository, under its one license.
const (
	sourceAnnotation  = "https://github.com/contemper-project/contemper"
	licenseAnnotation = "Apache-2.0"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buildimg:", err)
		os.Exit(1)
	}
}

func run() error {
	var prefix, tag, supportDir, revision, digestsFile string
	flag.StringVar(&prefix, "prefix", "", "registry prefix, e.g. ghcr.io/contemper-project or localhost:5555/contemper-e2e")
	flag.StringVar(&tag, "tag", "v1", "tag to publish the images under")
	flag.StringVar(&supportDir, "support-dir", "", "path to support/volumes-support (containing base/, openrc/, systemd/)")
	flag.StringVar(&revision, "revision", "", "commit SHA to record as org.opencontainers.image.revision (optional)")
	flag.StringVar(&digestsFile, "digests-file", "", "if set, append one '<image-name> <digest>' line per pushed image to this file (created if missing), for a caller that wants each image's digest without scraping stdout")
	flag.Parse()

	if prefix == "" {
		return fmt.Errorf("-prefix is required")
	}
	if supportDir == "" {
		return fmt.Errorf("-support-dir is required")
	}

	openrcName := prefix + "/volumes-support-init-system-openrc"
	openrcRef, openrcDigest, err := buildAndPush(filepath.Join(supportDir, "openrc"), openrcName+":"+tag, nil,
		ociAnnotations("volumes-support-init-system-openrc", revision,
			"OpenRC integration for contemper's volume-formatting boot helper: runs it as an init.d service before local filesystems are mounted."))
	if err != nil {
		return fmt.Errorf("openrc variant: %w", err)
	}
	fmt.Printf("pushed %s@%s\n", openrcRef, openrcDigest)
	if err := appendDigest(digestsFile, openrcName, openrcDigest); err != nil {
		return fmt.Errorf("recording digest for openrc variant: %w", err)
	}

	systemdName := prefix + "/volumes-support-init-system-systemd"
	systemdRef, systemdDigest, err := buildAndPush(filepath.Join(supportDir, "systemd"), systemdName+":"+tag, nil,
		ociAnnotations("volumes-support-init-system-systemd", revision,
			"systemd integration for contemper's volume-formatting boot helper: runs it as a oneshot service before local filesystems are mounted."))
	if err != nil {
		return fmt.Errorf("systemd variant: %w", err)
	}
	fmt.Printf("pushed %s@%s\n", systemdRef, systemdDigest)
	if err := appendDigest(digestsFile, systemdName, systemdDigest); err != nil {
		return fmt.Errorf("recording digest for systemd variant: %w", err)
	}

	openrcByDigest := fmt.Sprintf("%s@%s", openrcName, openrcDigest)
	systemdByDigest := fmt.Sprintf("%s@%s", systemdName, systemdDigest)
	baseAnnotations := map[string]string{
		"io.contemper.branch.init-system.openrc.requires.files":  "/sbin/openrc",
		"io.contemper.branch.init-system.openrc.image":           openrcByDigest,
		"io.contemper.branch.init-system.systemd.requires.files": "/usr/lib/systemd/systemd",
		"io.contemper.branch.init-system.systemd.image":          systemdByDigest,
	}
	baseName := prefix + "/volumes-support"
	baseRef, baseDigest, err := buildAndPush(filepath.Join(supportDir, "base"), baseName+":"+tag, baseAnnotations,
		ociAnnotations("volumes-support", revision,
			"contemper's volume-formatting boot helper: formats a blank declared volume and reuses an already-formatted one, leaving anything else alone."))
	if err != nil {
		return fmt.Errorf("base image: %w", err)
	}
	fmt.Printf("pushed %s@%s\n", baseRef, baseDigest)
	if err := appendDigest(digestsFile, baseName, baseDigest); err != nil {
		return fmt.Errorf("recording digest for base image: %w", err)
	}

	return nil
}

// appendDigest appends "name digest\n" to path, creating it if needed, so a
// caller (a CI workflow attesting build provenance, e.g.) can read back
// which digest each image name was last pushed at without parsing stdout.
// It does nothing when path is empty - the default, and what
// hack/e2e-volumes.sh relies on.
func appendDigest(path, name string, digest v1.Hash) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // G302: not a secret, just a scratch file this process's own caller reads back
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // best-effort close after a successful write below
	if _, err := fmt.Fprintf(f, "%s %s\n", name, digest); err != nil {
		return fmt.Errorf("writing to %s: %w", path, err)
	}
	return nil
}

// ociAnnotations builds the standard org.opencontainers.image.*
// annotations for one of the three published images: description (one
// short sentence specific to that image), source and licenses (the same
// for all three, since they're all built from this one repository), and
// title and revision when a commit SHA is available. ghcr.io shows at
// most a few hundred characters of description, so these stay well
// under that.
func ociAnnotations(title, revision, description string) map[string]string {
	anns := map[string]string{
		"org.opencontainers.image.title":       title,
		"org.opencontainers.image.description": description,
		"org.opencontainers.image.source":      sourceAnnotation,
		"org.opencontainers.image.licenses":    licenseAnnotation,
	}
	if revision != "" {
		anns["org.opencontainers.image.revision"] = revision
	}
	return anns
}

// buildIndex builds one platform image per entry in platforms from
// dir's file tree and appends each to a multi-platform index, with
// descAnnotations set on every one of its descriptors (the
// index-descriptor annotation fallback docs/reference/support-image-
// annotations.md describes, which applies uniformly regardless of which
// platform a reader resolves) and ociAnnotations set on both the index
// itself and each platform manifest - the two places ghcr.io reads
// image metadata from for a multi-platform reference. It does no
// network I/O, so it can be exercised without a registry.
func buildIndex(dir string, descAnnotations, ociAnns map[string]string) (v1.ImageIndex, error) {
	files, err := filesFromDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var idx v1.ImageIndex = empty.Index
	for _, arch := range platforms {
		platform := v1.Platform{OS: "linux", Architecture: arch}
		img, err := imgtest.Image(platform, nil, files)
		if err != nil {
			return nil, fmt.Errorf("building %s image: %w", arch, err)
		}
		if len(ociAnns) > 0 {
			img = mutate.Annotations(img, ociAnns).(v1.Image) //nolint:forcetypeassert // mutate.Annotations on a v1.Image always returns a v1.Image
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform:    &platform,
				Annotations: descAnnotations,
			},
		})
	}
	if len(ociAnns) > 0 {
		idx = mutate.Annotations(idx, ociAnns).(v1.ImageIndex) //nolint:forcetypeassert // mutate.Annotations on a v1.ImageIndex always returns a v1.ImageIndex
	}
	return idx, nil
}

// buildAndPush builds dir's multi-platform index (see buildIndex) and
// pushes it to ref.
func buildAndPush(dir, ref string, descAnnotations, ociAnns map[string]string) (string, v1.Hash, error) {
	idx, err := buildIndex(dir, descAnnotations, ociAnns)
	if err != nil {
		return "", v1.Hash{}, err
	}

	nref, err := name.ParseReference(ref)
	if err != nil {
		return "", v1.Hash{}, fmt.Errorf("parsing %q: %w", ref, err)
	}
	if err := remote.WriteIndex(nref, idx, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
		return "", v1.Hash{}, fmt.Errorf("pushing %s: %w", ref, err)
	}
	digest, err := idx.Digest()
	if err != nil {
		return "", v1.Hash{}, fmt.Errorf("reading digest of %s: %w", ref, err)
	}
	return ref, digest, nil
}

// filesFromDir walks dir and turns every entry into an imgtest.File,
// preserving its path relative to dir, its executable bit (mode 0755 vs
// 0644), and symlinks (read, not followed - this is how the systemd
// variant's *.wants enablement symlink is carried).
func filesFromDir(dir string) ([]imgtest.File, error) {
	var files []imgtest.File
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
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
		relSlash := filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("reading symlink %s: %w", path, err)
			}
			files = append(files, imgtest.File{Path: relSlash, Typeflag: tar.TypeSymlink, Linkname: target})
			return nil
		}

		if d.IsDir() {
			files = append(files, imgtest.File{Path: relSlash + "/", Typeflag: tar.TypeDir, Mode: 0o755})
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // G122: walks a fixed local fixture dir this dev tool controls, not an adversarial or attacker-writable tree
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		mode := int64(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		files = append(files, imgtest.File{Path: relSlash, Mode: mode, Data: data})
		return nil
	})
	return files, err
}
