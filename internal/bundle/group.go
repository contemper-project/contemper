package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/contemper-project/contemper/internal/source"
)

// GroupFormatVersion is the current <name>.multiarch.json schema
// version.
const GroupFormatVersion = 1

// GroupSuffix is appended to a bundle's name, without its architecture
// suffix, to name the group file a multi-architecture run writes.
const GroupSuffix = ".multiarch.json"

// GroupBundle is one architecture's bundle in a Group.
type GroupBundle struct {
	// Arch is the OCI architecture ("amd64", "arm64"), as in the
	// bundle's own manifest.
	Arch string `json:"arch"`
	// Path is the bundle directory, relative to the group file's own
	// directory.
	Path string `json:"path"`
}

// Group is the document convert writes next to the bundles of a
// multi-architecture run, so a script or deploy can find the bundle for
// a given architecture. See docs/reference/bundle.md.
type Group struct {
	FormatVersion int `json:"formatVersion"`
	// Source identifies the image the bundles came from. Each
	// architecture resolves to its own image, so Digest is left empty;
	// the per-architecture digests are in the bundles' manifests.
	Source  ImageRef      `json:"source"`
	Bundles []GroupBundle `json:"bundles"`
}

// GroupFileName returns the group file name for a bundle name given
// without its architecture suffix, e.g. "my-app-v3" ->
// "my-app-v3.multiarch.json".
func GroupFileName(name string) string { return name + GroupSuffix }

// Find returns the bundle directory recorded for arch, relative to the
// group file's directory, and whether there is one.
func (g *Group) Find(arch string) (string, bool) {
	for _, b := range g.Bundles {
		if b.Arch == arch {
			return b.Path, true
		}
	}
	return "", false
}

func (g *Group) check() error {
	if g.FormatVersion != GroupFormatVersion {
		return fmt.Errorf("unsupported formatVersion %d (want %d)", g.FormatVersion, GroupFormatVersion)
	}
	if len(g.Bundles) == 0 {
		return fmt.Errorf("no bundles listed")
	}
	seen := map[string]bool{}
	paths := map[string]bool{}
	for _, b := range g.Bundles {
		if b.Arch == "" {
			return fmt.Errorf("bundle %q has no arch", b.Path)
		}
		if !slices.Contains(source.SupportedArchs, b.Arch) {
			return fmt.Errorf("bundle %q: unsupported arch %q (want one of %s)", b.Path, b.Arch, strings.Join(source.SupportedArchs, ", "))
		}
		if seen[b.Arch] {
			return fmt.Errorf("arch %q listed more than once", b.Arch)
		}
		seen[b.Arch] = true
		// A group file may have been copied from elsewhere: each path
		// must stay inside the directory the file sits in.
		p := filepath.FromSlash(b.Path)
		if b.Path == "" || b.Path == "." || strings.Contains(b.Path, "\\") || filepath.IsAbs(p) || !filepath.IsLocal(p) {
			return fmt.Errorf("bundle path %q must be a relative path inside the group file's directory", b.Path)
		}
		clean := filepath.ToSlash(filepath.Clean(p))
		if paths[clean] {
			return fmt.Errorf("bundle path %q listed more than once", b.Path)
		}
		paths[clean] = true
	}
	return nil
}

// WriteGroup validates g and writes it as indented JSON to
// <dir>/<name>.multiarch.json (name without any architecture suffix),
// replacing a previous file of that name, and returns the file's path.
func WriteGroup(dir, name string, g *Group) (string, error) {
	if err := g.check(); err != nil {
		return "", fmt.Errorf("group file: %w", err)
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling group file: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, GroupFileName(name))
	// Write beside the target and rename, so a reader never sees a
	// half-written file. Like contemper.json, it is a deliverable meant
	// to stay world-readable.
	tmp, err := os.CreateTemp(dir, ".multiarch-*.json")
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // G302: group file is a bundle output meant to stay world-readable
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return path, nil
}

// ReadGroup parses and validates the group file at path.
func ReadGroup(path string) (*Group, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var g Group
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := g.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &g, nil
}

// RemoveGroup deletes the group file for name in dir, if there is one,
// so a failed run doesn't leave one describing bundles from an earlier
// run.
func RemoveGroup(dir, name string) error {
	err := os.Remove(filepath.Join(dir, GroupFileName(name)))
	// ENOTDIR: dir is itself a file, so there is no group file in it.
	if err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
		return fmt.Errorf("removing old group file: %w", err)
	}
	return nil
}
