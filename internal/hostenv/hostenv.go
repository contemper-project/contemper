// Package hostenv discovers the host tools contemper shells out to
// (mkfs.ext4, debugfs, e2fsck, qemu-img, qemu-system-*), looking on PATH
// first and then in well-known Homebrew keg-only locations, and prints
// an install hint when a tool is missing.
package hostenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func isDarwin() bool { return runtime.GOOS == "darwin" }

// extraDirs lists additional directories to search for a tool keyed by
// tool name, beyond what PATH already covers. e2fsprogs is keg-only on
// Homebrew (it conflicts with macOS's own, older e2fsprogs-less
// toolchain), so its sbin is not linked into a normal PATH.
var extraDirs = map[string][]string{
	"mkfs.ext4": e2fsprogsDirs(),
	"debugfs":   e2fsprogsDirs(),
	"e2fsck":    e2fsprogsDirs(),
	"qemu-img":  qemuDirs(),
}

func e2fsprogsDirs() []string {
	return []string{
		"/opt/homebrew/opt/e2fsprogs/sbin",
		"/opt/homebrew/opt/e2fsprogs/bin",
		"/usr/local/opt/e2fsprogs/sbin",
		"/usr/local/opt/e2fsprogs/bin",
		"/usr/sbin",
	}
}

func qemuDirs() []string {
	return []string{
		"/opt/homebrew/opt/qemu/bin",
		"/usr/local/opt/qemu/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
}

// qemuSystemDirs returns the search dirs for a qemu-system-* binary,
// which is architecture-specific and so is not keyed statically in
// extraDirs.
func qemuSystemDirs() []string { return qemuDirs() }

// Find locates name on PATH, then in name's extra known locations.
// It returns the resolved absolute path, or "" if name could not be found
// anywhere.
func Find(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	dirs := extraDirs[name]
	if dirs == nil && len(name) >= len("qemu-system-") && name[:len("qemu-system-")] == "qemu-system-" {
		dirs = qemuSystemDirs()
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// InstallHint returns a short, human-readable suggestion for installing
// name, appropriate to the current GOOS.
func InstallHint(name string) string {
	pkg := packageFor(name)
	if isDarwin() {
		return fmt.Sprintf("brew install %s", pkg)
	}
	return fmt.Sprintf("apt install %s", pkg)
}

func packageFor(name string) string {
	switch name {
	case "mkfs.ext4", "debugfs", "e2fsck":
		return "e2fsprogs"
	case "qemu-img":
		if isDarwin() {
			return "qemu"
		}
		return "qemu-utils"
	default:
		if len(name) >= len("qemu-system-") && name[:len("qemu-system-")] == "qemu-system-" {
			if isDarwin() {
				return "qemu"
			}
			return "qemu-system-arm qemu-system-x86"
		}
		return name
	}
}

// Required finds name or returns an error naming the install hint.
func Required(name string) (string, error) {
	if p := Find(name); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("%s not found on PATH; install it with: %s", name, InstallHint(name))
}
