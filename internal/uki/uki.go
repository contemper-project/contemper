// Package uki builds a Unified Kernel Image: a systemd-stub UEFI binary
// with .osrel, .cmdline, .initrd and .linux sections appended, in that
// order, the way systemd-ukify lays them out. It never shells out; the
// stub binaries are embedded and the PE surgery is done in pure Go.
package uki

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"fmt"
	"io"
)

//go:embed stubs/linuxaa64.efi.stub
var stubARM64 []byte

//go:embed stubs/linuxx64.efi.stub
var stubAMD64 []byte

// Sections holds the content for each UKI section. OSRelease may be nil,
// in which case no .osrel section is added (it's optional per the fixed
// -path contract). Cmdline, Initrd and Linux must be non-empty.
type Sections struct {
	OSRelease []byte
	Cmdline   []byte
	Initrd    []byte
	Linux     []byte
}

// StubFor returns the embedded systemd-stub bytes for arch ("arm64" or
// "amd64").
func StubFor(arch string) ([]byte, error) {
	switch arch {
	case "arm64":
		return stubARM64, nil
	case "amd64":
		return stubAMD64, nil
	default:
		return nil, fmt.Errorf("no UKI stub embedded for arch %q", arch)
	}
}

// Build returns a UKI: the systemd-stub for arch with sections appended.
func Build(arch string, sections Sections) ([]byte, error) {
	if len(sections.Cmdline) == 0 {
		return nil, fmt.Errorf("cmdline section must not be empty")
	}
	if len(sections.Initrd) == 0 {
		return nil, fmt.Errorf("initrd section must not be empty")
	}
	if len(sections.Linux) == 0 {
		return nil, fmt.Errorf("linux (kernel) section must not be empty")
	}

	stub, err := StubFor(arch)
	if err != nil {
		return nil, err
	}

	var named []namedSection
	if len(sections.OSRelease) > 0 {
		named = append(named, namedSection{Name: ".osrel", Data: sections.OSRelease})
	}
	named = append(named,
		namedSection{Name: ".cmdline", Data: sections.Cmdline},
		namedSection{Name: ".initrd", Data: sections.Initrd},
		namedSection{Name: ".linux", Data: sections.Linux},
	)

	out, err := appendSections(stub, named)
	if err != nil {
		return nil, fmt.Errorf("building UKI for %s: %w", arch, err)
	}
	return out, nil
}

// PrepareKernel normalizes a kernel image for embedding as the .linux
// section: an MZ/PE-format kernel (a bzImage, or an arm64 Image already
// wrapped as an EFI application) is used as-is; a gzip-compressed image
// is decompressed. Anything else is passed through unmodified, with a
// non-empty warning describing why.
func PrepareKernel(data []byte) (out []byte, warning string, err error) {
	if len(data) >= 2 && data[0] == 'M' && data[1] == 'Z' {
		return data, "", nil
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("decompressing gzip kernel: %w", err)
		}
		defer func() { _ = zr.Close() }()
		decompressed, err := io.ReadAll(zr)
		if err != nil {
			return nil, "", fmt.Errorf("decompressing gzip kernel: %w", err)
		}
		return decompressed, "", nil
	}
	return data, "kernel image is neither MZ/PE nor gzip-compressed; embedding it unmodified", nil
}
