// Package guestmeta renders the convert-time-only facts contemper writes
// into a bundle's guest filesystem under /etc/contemper/, and the fstab
// lines it appends for declared volumes. Everything here is convert-time
// data: what image, target and support layers produced this disk. It
// never carries deployment-time data, which stays with the provider's
// own metadata channel.
//
// See docs/design/volumes-and-providers.md for why these facts exist and
// docs/reference/bundle.md and the volumes guide for the file formats.
package guestmeta

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// Fixed paths contemper writes into every rootfs.
const (
	BuildPath   = "/etc/contemper/build"
	VolumesPath = "/etc/contemper/volumes"
	FstabPath   = "/etc/fstab"
)

// ImageRef is a plain ref+digest pair, already redacted by the caller for
// local sources (scheme + file name only, no directories - see
// docs/reference/bundle.md's note on source.repo/source.ref) before it
// reaches this package.
type ImageRef struct {
	Ref    string
	Digest string
}

// pullSpec combines ref and digest into a single "ref@digest" pull spec
// for /etc/contemper/build, unless ref is already digest-qualified (a
// variant annotation can point at its image either way - see
// docs/reference/support-image-annotations.md - and a support image
// that pins its own variants by digest would otherwise end up with both
// appended, e.g. "...@sha256:x@sha256:x").
func pullSpec(ref, digest string) string {
	if strings.Contains(ref, "@") {
		return ref
	}
	return ref + "@" + digest
}

// RedactLocalRef returns the string to record in /etc/contemper/build for
// a local, non-registry source or support reference: scheme plus the
// file's base name only, with every directory component stripped, so no
// path from the build machine ends up in the guest. scheme is the source
// kind prefix as ParseRef accepts it ("oci-archive", "oci",
// "docker-archive"); value is the local path. Registry references need no
// redaction and should be recorded in full instead of through this
// function.
func RedactLocalRef(scheme, value string) string {
	return scheme + ":" + filepath.Base(value)
}

// VariantRef records one resolved branch for /etc/contemper/build: the
// branch and variant name, and (unless the variant was a no-op) the
// image whose layers it contributed.
type VariantRef struct {
	Branch, Variant string
	// Ref and Digest are empty for a no-op variant, which contributes
	// nothing to record.
	Ref, Digest string
}

// BuildInfo carries every fact known at convert time that
// /etc/contemper/build records.
type BuildInfo struct {
	ContemperVersion string
	Target           string
	Arch             string
	Source           ImageRef
	// Support and SupportVariants describe the user-specified --support
	// image, if any.
	Support         *ImageRef
	SupportVariants []VariantRef
	// VolumeHelper and VolumeHelperVariants describe the automatically
	// merged volume-formatting helper, if any.
	VolumeHelper         *ImageRef
	VolumeHelperVariants []VariantRef
}

// RenderBuild renders info as the key=value lines of /etc/contemper/build.
// There is deliberately no timestamp: the same inputs always produce the
// same output, so the file doesn't compromise a reproducible build.
func RenderBuild(info BuildInfo) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "contemper.version=%s\n", info.ContemperVersion)
	fmt.Fprintf(&b, "target=%s\n", info.Target)
	fmt.Fprintf(&b, "arch=%s\n", info.Arch)
	fmt.Fprintf(&b, "source.ref=%s\n", info.Source.Ref)
	fmt.Fprintf(&b, "source.digest=%s\n", info.Source.Digest)
	if info.Support != nil {
		fmt.Fprintf(&b, "support.ref=%s\n", info.Support.Ref)
		fmt.Fprintf(&b, "support.digest=%s\n", info.Support.Digest)
	}
	for _, v := range info.SupportVariants {
		if v.Ref == "" {
			continue
		}
		fmt.Fprintf(&b, "support.variant.%s=%s\n", v.Branch, pullSpec(v.Ref, v.Digest))
	}
	if info.VolumeHelper != nil {
		fmt.Fprintf(&b, "volume-helper.ref=%s\n", info.VolumeHelper.Ref)
		fmt.Fprintf(&b, "volume-helper.digest=%s\n", info.VolumeHelper.Digest)
	}
	for _, v := range info.VolumeHelperVariants {
		if v.Ref == "" {
			continue
		}
		fmt.Fprintf(&b, "volume-helper.variant.%s=%s\n", v.Branch, pullSpec(v.Ref, v.Digest))
	}
	return []byte(b.String())
}

// VolumeLine is one line of /etc/contemper/volumes: a volume's name, the
// serial pattern its target uses to find the disk, its filesystem, and
// its mount point (last, since it may contain spaces).
type VolumeLine struct {
	Name          string
	SerialPattern string
	FS            string
	Mountpoint    string
}

// RenderVolumes renders lines as the space-separated rows of
// /etc/contemper/volumes, one volume per line, mount point last.
func RenderVolumes(lines []VolumeLine) []byte {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s %s %s %s\n", l.Name, l.SerialPattern, l.FS, l.Mountpoint)
	}
	return []byte(b.String())
}

// FstabLine renders the fstab line contemper appends for one volume.
func FstabLine(name, path string) string {
	return fmt.Sprintf("LABEL=%s %s ext4 defaults,nofail 0 2", name, path)
}

// AppendFstab returns fstab's new full content with any line in lines
// that isn't already present appended, preserving existing content
// (and its own line ordering) untouched. changed is false when every
// line was already present, so the caller can skip rewriting the file
// entirely. A missing trailing newline on existing content is added
// before appending.
func AppendFstab(existing []byte, lines []string) (updated []byte, changed bool) {
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[l] = true
	}

	var b bytes.Buffer
	b.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, l := range lines {
		if have[l] {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
		changed = true
	}
	return b.Bytes(), changed
}
