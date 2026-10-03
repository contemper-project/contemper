// Package validate checks the merged rootfs against contemper's fixed
// path contract: a kernel, an initrd and an init binary, plus an
// optional command line, at fixed locations.
package validate

import (
	"fmt"
	"strings"

	"github.com/contemper-project/contemper/internal/rootfs"
)

// Fixed paths every contemper target relies on.
const (
	KernelPath    = "/boot/contemper/vmlinuz"
	InitrdPath    = "/boot/contemper/initrd"
	CmdlinePath   = "/boot/contemper/cmdline"
	InitPath      = "/sbin/init"
	OSReleasePath = "/etc/os-release"
)

// Result carries the file content read while validating the contract, so
// callers (the UKI builder) do not need to re-read the rootfs.
type Result struct {
	Kernel    []byte
	Initrd    []byte
	Cmdline   string
	OSRelease []byte // nil if the image has none
	// Bootloader is set instead of the fields above for an image that
	// brings its own bootloader (see CheckBootloader).
	Bootloader *Bootloader
}

// Validate checks rfs against the fixed-path contract and returns the
// file contents needed to build the UKI.
func Validate(rfs *rootfs.Rootfs) (*Result, error) {
	kernel, err := rfs.ReadFile(KernelPath)
	if err != nil {
		return nil, fmt.Errorf("kernel: %w", err)
	}
	initrd, err := rfs.ReadFile(InitrdPath)
	if err != nil {
		return nil, fmt.Errorf("initrd: %w", err)
	}
	var cmdline string
	if present(rfs, CmdlinePath) {
		cmdlineRaw, err := rfs.ReadFile(CmdlinePath)
		if err != nil {
			return nil, fmt.Errorf("cmdline: %w", err)
		}
		cmdline = strings.TrimSpace(string(cmdlineRaw))
	}

	if _, err := rfs.Resolve(InitPath); err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}

	var osRelease []byte
	if present(rfs, OSReleasePath) {
		osRelease, err = rfs.ReadFile(OSReleasePath)
		if err != nil {
			return nil, fmt.Errorf("os-release: %w", err)
		}
	}

	return &Result{Kernel: kernel, Initrd: initrd, Cmdline: cmdline, OSRelease: osRelease}, nil
}

// present reports whether an optional fixed path exists: either as an
// entry of its own (even a dangling symlink, so reading it reports the
// problem) or reached through a symlinked parent directory, such as a
// /boot/contemper that is itself a symlink.
func present(rfs *rootfs.Rootfs, p string) bool {
	if _, ok := rfs.Lookup(p); ok {
		return true
	}
	_, err := rfs.Resolve(p)
	return err == nil
}
