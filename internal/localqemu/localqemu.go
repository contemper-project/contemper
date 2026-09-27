// Package localqemu implements the per-instance state `deploy --to
// local-qemu` keeps between deploys: a state directory, and one qcow2
// file per declared volume inside it, created once and reused on later
// deploys while the root disk itself resets every time (booted with
// qemu's snapshot=on).
//
// See docs/design/volumes-and-providers.md and docs/guide/deploying.md.
package localqemu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/contemper-project/contemper/internal/hostenv"
)

// ValidateInstanceName checks that name is safe to use as a single path
// component: non-empty, no path separator, not "." or "..".
func ValidateInstanceName(name string) error {
	if name == "" {
		return fmt.Errorf("instance name is empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("instance name %q is not allowed", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("instance name %q must not contain a path separator", name)
	}
	return nil
}

// DefaultInstance returns the default local-qemu instance name for a
// bundle whose source image's repository (no tag) is repo: deploying a
// new tag of the same image reuses the same instance, and so its
// volumes. --name overrides this.
func DefaultInstance(repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("the bundle has no recorded source repository to name the instance after; pass --name")
	}
	if err := ValidateInstanceName(repo); err != nil {
		return "", fmt.Errorf("source repository %q is not a valid instance name: %w; pass --name", repo, err)
	}
	return repo, nil
}

// StateDir returns the per-instance state directory:
// $XDG_STATE_HOME/contemper/local-qemu/<instance>, or
// $HOME/.local/state/contemper/local-qemu/<instance> if XDG_STATE_HOME
// isn't set.
func StateDir(instance string) (string, error) {
	if err := ValidateInstanceName(instance); err != nil {
		return "", err
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving the home directory for the default state dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "contemper", "local-qemu", instance), nil
}

// VolumeDiskAction is what planVolumeDisk decided EnsureVolumeDisk should
// do for one volume's disk file.
type VolumeDiskAction struct {
	Path   string
	Create bool // true: the file doesn't exist yet and must be created
}

// planVolumeDisk is EnsureVolumeDisk's decision logic, pure and so
// testable without qemu-img: exists and existingSize describe the
// current state of dir/<name>.qcow2 (existingSize is meaningless when
// exists is false).
func planVolumeDisk(dir, name string, sizeBytes int64, exists bool, existingSize int64) (VolumeDiskAction, error) {
	path := filepath.Join(dir, name+".qcow2")
	if !exists {
		return VolumeDiskAction{Path: path, Create: true}, nil
	}
	if existingSize != sizeBytes {
		return VolumeDiskAction{}, fmt.Errorf(
			"volume %q already has a disk at %s (%d bytes), which does not match the requested size (%d bytes); "+
				"contemper does not resize an existing volume - remove the file to start over, or deploy without changing its size",
			name, path, existingSize, sizeBytes)
	}
	return VolumeDiskAction{Path: path, Create: false}, nil
}

// EnsureVolumeDisk creates dir/<name>.qcow2 sized sizeBytes if it doesn't
// exist yet (via `qemu-img create`), or, if it already exists, checks its
// virtual size against sizeBytes and refuses (returns an error) rather
// than resizing it silently: contemper never touches an existing
// volume's size, on either a request to grow it (which would leave the
// filesystem inside it the old size until something resizes it) or
// shrink it (which could truncate data). Returns the disk's path and
// whether it was just created.
func EnsureVolumeDisk(dir, name string, sizeBytes int64) (path string, created bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("creating state dir %s: %w", dir, err)
	}

	candidate := filepath.Join(dir, name+".qcow2")
	_, statErr := os.Stat(candidate)
	exists := statErr == nil
	var existingSize int64
	if exists {
		existingSize, err = qcow2VirtualSize(candidate)
		if err != nil {
			return "", false, err
		}
	} else if !os.IsNotExist(statErr) {
		return "", false, fmt.Errorf("checking %s: %w", candidate, statErr)
	}

	action, err := planVolumeDisk(dir, name, sizeBytes, exists, existingSize)
	if err != nil {
		return "", false, err
	}
	if !action.Create {
		return action.Path, false, nil
	}

	qemuImgPath, err := hostenv.Required("qemu-img")
	if err != nil {
		return "", false, err
	}
	args := []string{"create", "-f", "qcow2", action.Path, strconv.FormatInt(sizeBytes, 10)}
	if out, err := exec.Command(qemuImgPath, args...).CombinedOutput(); err != nil {
		return "", false, fmt.Errorf("qemu-img create: %w\n%s", err, out)
	}
	return action.Path, true, nil
}

// qcow2VirtualSize returns the virtual (guest-visible) size in bytes of
// the qcow2 image at path, parsed from `qemu-img info --output=json`
// (structured output only, per the provider-tooling conventions in
// docs/design/volumes-and-providers.md).
func qcow2VirtualSize(path string) (int64, error) {
	qemuImgPath, err := hostenv.Required("qemu-img")
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(qemuImgPath, "info", "--output=json", path)
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("qemu-img info %s: %w\n%s", path, err, strings.TrimSpace(stderr.String()))
	}
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, fmt.Errorf("parsing qemu-img info output for %s: %w", path, err)
	}
	return info.VirtualSize, nil
}
