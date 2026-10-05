package uki

import (
	"encoding/binary"
	"testing"
)

// minimalPE returns a small PE32+ image with one .text section, room for
// extra section headers in its header area, and the section data ending
// exactly at EOF, the shape appendSections expects of a stub.
func minimalPE() []byte {
	const (
		optNumberOfRvaAndSizes = 108 // offset of NumberOfRvaAndSizes in the PE32+ optional header
		lfanew                 = 0x40
		optSize                = 0xF0
		hdrSize                = 0x200
		rawSize                = 0x200
	)
	b := make([]byte, hdrSize+rawSize)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[peSignatureOffset:], lfanew)
	copy(b[lfanew:], "PE\x00\x00")
	coff := lfanew + 4
	binary.LittleEndian.PutUint16(b[coff+coffMachine:], 0x8664)
	binary.LittleEndian.PutUint16(b[coff+coffNumberOfSections:], 1)
	binary.LittleEndian.PutUint16(b[coff+coffSizeOfOptionalHeader:], optSize)
	opt := coff + coffHeaderSize
	binary.LittleEndian.PutUint16(b[opt+optMagic:], pe32PlusMagic)
	binary.LittleEndian.PutUint32(b[opt+optSectionAlignment:], 0x1000)
	binary.LittleEndian.PutUint32(b[opt+optFileAlignment:], 0x200)
	binary.LittleEndian.PutUint32(b[opt+optSizeOfImage:], 0x2000)
	binary.LittleEndian.PutUint32(b[opt+optSizeOfHeaders:], hdrSize)
	binary.LittleEndian.PutUint32(b[opt+optNumberOfRvaAndSizes:], 16)
	sec := opt + optSize
	copy(b[sec+shName:], ".text")
	binary.LittleEndian.PutUint32(b[sec+shVirtualSize:], 0x10)
	binary.LittleEndian.PutUint32(b[sec+shVirtualAddress:], 0x1000)
	binary.LittleEndian.PutUint32(b[sec+shSizeOfRawData:], rawSize)
	binary.LittleEndian.PutUint32(b[sec+shPointerToRawData:], hdrSize)
	return b
}

func TestParsePERejectsImplausibleAlignment(t *testing.T) {
	const opt = 0x40 + 4 + coffHeaderSize // optional header offset in minimalPE
	if _, err := parsePE(minimalPE()); err != nil {
		t.Fatalf("parsePE(minimalPE()): %v", err)
	}
	for name, edit := range map[string]func(b []byte){
		"file alignment not a power of two": func(b []byte) { binary.LittleEndian.PutUint32(b[opt+optFileAlignment:], 0x300) },
		"file alignment of 2 GiB":           func(b []byte) { binary.LittleEndian.PutUint32(b[opt+optFileAlignment:], 1<<31) },
		"section alignment not a power":     func(b []byte) { binary.LittleEndian.PutUint32(b[opt+optSectionAlignment:], 0x1800) },
		"section alignment of 2 GiB":        func(b []byte) { binary.LittleEndian.PutUint32(b[opt+optSectionAlignment:], 1<<31) },
	} {
		b := minimalPE()
		edit(b)
		if _, err := parsePE(b); err == nil {
			t.Errorf("%s: parsePE accepted it", name)
		}
		if _, err := appendSections(b, []namedSection{{".x", []byte("x")}}); err == nil {
			t.Errorf("%s: appendSections accepted it", name)
		}
	}
}

func TestParsePERejectsMissingMZ(t *testing.T) {
	b := minimalPE()
	b[0], b[1] = 0, 0
	if _, err := parsePE(b); err == nil {
		t.Error("parsePE accepted an image without the MZ signature")
	}
	if _, err := appendSections(b, []namedSection{{".x", []byte("x")}}); err == nil {
		t.Error("appendSections accepted an image without the MZ signature")
	}
}

// New sections start where the last one's raw data ends, so a stub whose
// raw data does not end on a file alignment boundary cannot be extended
// without breaking the alignment of what follows.
func TestAppendSectionsRejectsUnalignedStubEnd(t *testing.T) {
	const opt = 0x40 + 4 + coffHeaderSize
	b := minimalPE() // ends at 0x400
	binary.LittleEndian.PutUint32(b[opt+optFileAlignment:], 0x2000)
	binary.LittleEndian.PutUint32(b[opt+optSectionAlignment:], 0x2000)
	if _, err := appendSections(b, []namedSection{{".x", []byte("x")}}); err == nil {
		t.Error("appendSections accepted a stub whose raw data ends off the file alignment")
	}
	if _, err := appendSections(minimalPE(), []namedSection{{".x", []byte("x")}}); err != nil {
		t.Errorf("appendSections(minimalPE()): %v", err)
	}
}

func TestAppendSectionsRejectsStringTableName(t *testing.T) {
	if _, err := appendSections(minimalPE(), []namedSection{{"/4", []byte("x")}}); err == nil {
		t.Error("appendSections accepted a section name that is a string table reference")
	}
}
