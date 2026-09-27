package uki_test

import (
	"bytes"
	"compress/gzip"
	"debug/pe"
	"testing"

	"github.com/contemper-project/contemper/internal/uki"
)

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPrepareKernel(t *testing.T) {
	mz := append([]byte("MZ"), bytes.Repeat([]byte{0}, 100)...)
	out, warn, err := uki.PrepareKernel(mz)
	if err != nil || warn != "" || !bytes.Equal(out, mz) {
		t.Errorf("MZ kernel: out equal=%v warn=%q err=%v", bytes.Equal(out, mz), warn, err)
	}

	raw := []byte("raw kernel image bytes")
	gz := gzipBytes(t, raw)
	out, warn, err = uki.PrepareKernel(gz)
	if err != nil || warn != "" || !bytes.Equal(out, raw) {
		t.Errorf("gzip kernel: out=%q warn=%q err=%v", out, warn, err)
	}

	weird := []byte("neither MZ nor gzip")
	out, warn, err = uki.PrepareKernel(weird)
	if err != nil || warn == "" || !bytes.Equal(out, weird) {
		t.Errorf("weird kernel: out equal=%v warn=%q err=%v", bytes.Equal(out, weird), warn, err)
	}
}

func testBuildAndRoundTrip(t *testing.T, arch string) {
	t.Helper()

	sections := uki.Sections{
		OSRelease: []byte("NAME=Test\nID=test\n"),
		Cmdline:   []byte("root=LABEL=contemper-root rw console=ttyAMA0"),
		Initrd:    bytes.Repeat([]byte{0xAB}, 4096+37), // spans more than one FileAlignment block
		Linux:     append([]byte("not-a-real-kernel-but-nonempty"), bytes.Repeat([]byte{0xCD}, 8192)...),
	}

	out, err := uki.Build(arch, sections)
	if err != nil {
		t.Fatalf("Build(%s): %v", arch, err)
	}

	f, err := pe.NewFile(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("debug/pe could not parse the built UKI: %v", err)
	}
	defer func() { _ = f.Close() }()

	want := map[string][]byte{
		".osrel":   sections.OSRelease,
		".cmdline": sections.Cmdline,
		".initrd":  sections.Initrd,
		".linux":   sections.Linux,
	}

	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Fatalf("expected a PE32+ optional header, got %T", f.OptionalHeader)
	}

	var prevEnd uint32
	sawWanted := map[string]bool{}
	for _, sec := range f.Sections {
		if sec.VirtualAddress < prevEnd {
			t.Errorf("section %s VA %#x overlaps previous section end %#x", sec.Name, sec.VirtualAddress, prevEnd)
		}
		if sec.VirtualAddress%oh.SectionAlignment != 0 {
			t.Errorf("section %s VA %#x is not aligned to SectionAlignment %#x", sec.Name, sec.VirtualAddress, oh.SectionAlignment)
		}
		if sec.Offset%oh.FileAlignment != 0 {
			t.Errorf("section %s raw offset %#x is not aligned to FileAlignment %#x", sec.Name, sec.Offset, oh.FileAlignment)
		}
		prevEnd = sec.VirtualAddress + sec.Size

		wantData, isNew := want[sec.Name]
		if !isNew {
			continue
		}
		sawWanted[sec.Name] = true
		gotData, err := sec.Data()
		if err != nil {
			t.Fatalf("reading section %s: %v", sec.Name, err)
		}
		gotData = gotData[:sec.VirtualSize]
		if !bytes.Equal(gotData, wantData) {
			t.Errorf("section %s content mismatch: got %d bytes, want %d bytes", sec.Name, len(gotData), len(wantData))
		}
	}
	for name := range want {
		if !sawWanted[name] {
			t.Errorf("section %s not found in built UKI", name)
		}
	}

	if oh.SizeOfImage < prevEnd {
		t.Errorf("SizeOfImage %#x is less than the last section's end %#x", oh.SizeOfImage, prevEnd)
	}
	if oh.CheckSum != 0 {
		t.Errorf("CheckSum should be zeroed, got %#x", oh.CheckSum)
	}
}

func TestBuildARM64(t *testing.T) { testBuildAndRoundTrip(t, "arm64") }
func TestBuildAMD64(t *testing.T) { testBuildAndRoundTrip(t, "amd64") }

func TestBuildRejectsMissingSections(t *testing.T) {
	_, err := uki.Build("arm64", uki.Sections{})
	if err == nil {
		t.Fatalf("expected an error for empty sections")
	}
}

func TestBuildUnknownArch(t *testing.T) {
	_, err := uki.Build("riscv64", uki.Sections{
		Cmdline: []byte("x"), Initrd: []byte("x"), Linux: []byte("x"),
	})
	if err == nil {
		t.Fatalf("expected an error for an unknown arch")
	}
}
