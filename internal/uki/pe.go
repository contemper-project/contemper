package uki

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// Minimal PE32+ (64-bit) reader/writer sufficient for appending sections
// to a systemd-stub UEFI binary. We deliberately don't use debug/pe: the
// standard library's implementation is read-only, and what we need here
// - editing a handful of header fields and appending section headers and
// raw data - is small enough to implement directly and test by round
// -tripping through debug/pe afterwards.

const (
	peSignatureOffset = 0x3C // offset of the e_lfanew field in the DOS header
	pe32PlusMagic     = 0x20b

	coffHeaderSize = 20
	// Offsets within the COFF file header (relative to its start).
	coffMachine              = 0
	coffNumberOfSections     = 2
	coffSizeOfOptionalHeader = 16

	// Offsets within the PE32+ optional header (relative to its start).
	optMagic                   = 0
	optSizeOfCode              = 4
	optSizeOfInitializedData   = 8
	optSizeOfUninitializedData = 12
	optSectionAlignment        = 32
	optFileAlignment           = 36
	optSizeOfImage             = 56
	optSizeOfHeaders           = 60
	optCheckSum                = 64

	sectionHeaderSize = 40
	// Offsets within a section header (relative to its start).
	shName             = 0
	shNameLen          = 8
	shVirtualSize      = 8
	shVirtualAddress   = 12
	shSizeOfRawData    = 16
	shPointerToRawData = 20
	shCharacteristics  = 36

	// scnCntInitializedData | scnMemRead: a read-only, initialized-data
	// section - what ukify uses for the sections it appends.
	sectionCharacteristics = 0x40000040
)

// peImage is a parsed view over a PE32+ file's headers, backed by the
// original byte slice so unrelated bytes (the DOS stub, existing section
// raw data) are preserved unless explicitly mutated.
type peImage struct {
	buf []byte

	lfanew      int
	coffOff     int
	optOff      int
	optSize     int
	sectionsOff int // offset of the first section header

	numSections   int
	sectionAlign  uint32
	fileAlign     uint32
	sizeOfHeaders uint32
}

const (
	// maxFileAlignment is the PE specification's largest FileAlignment
	// (64 KiB). appendSections pads every new section's raw data to a
	// multiple of it, so a header claiming more would be a huge
	// allocation for a few bytes of content.
	maxFileAlignment = 1 << 16
	// maxSectionAlignment bounds SectionAlignment, which the
	// specification defaults to the page size (at most 64 KiB in
	// practice); 1 MiB is far above any real value and keeps the virtual
	// address arithmetic away from the 32-bit limit.
	maxSectionAlignment = 1 << 20
)

// plausibleAlignment reports whether a is zero (no alignment) or a power
// of two no larger than limit.
func plausibleAlignment(a, limit uint32) bool {
	return a == 0 || (a&(a-1) == 0 && a <= limit)
}

func parsePE(data []byte) (*peImage, error) {
	if len(data) < peSignatureOffset+4 {
		return nil, fmt.Errorf("too small to be a PE image")
	}
	// debug/pe only follows e_lfanew when the file starts with "MZ" and
	// otherwise reads a bare COFF object from offset 0, so an image
	// without the DOS signature would be a different file to it than to us.
	if data[0] != 'M' || data[1] != 'Z' {
		return nil, fmt.Errorf("missing MZ signature")
	}
	lfanew := int(binary.LittleEndian.Uint32(data[peSignatureOffset:]))
	if lfanew < 0 || lfanew+4+coffHeaderSize > len(data) {
		return nil, fmt.Errorf("invalid e_lfanew %#x", lfanew)
	}
	if string(data[lfanew:lfanew+4]) != "PE\x00\x00" {
		return nil, fmt.Errorf("missing PE signature at %#x", lfanew)
	}
	coffOff := lfanew + 4
	numSections := int(binary.LittleEndian.Uint16(data[coffOff+coffNumberOfSections:]))
	optSize := int(binary.LittleEndian.Uint16(data[coffOff+coffSizeOfOptionalHeader:]))
	optOff := coffOff + coffHeaderSize
	if optOff+optSize > len(data) {
		return nil, fmt.Errorf("optional header overruns file")
	}
	if optSize < optCheckSum+4 {
		return nil, fmt.Errorf("optional header too small (%d bytes)", optSize)
	}
	magic := binary.LittleEndian.Uint16(data[optOff+optMagic:])
	if magic != pe32PlusMagic {
		return nil, fmt.Errorf("unsupported optional header magic %#x (only PE32+ is supported)", magic)
	}
	sectionsOff := optOff + optSize
	if sectionsOff+numSections*sectionHeaderSize > len(data) {
		return nil, fmt.Errorf("section header table overruns file")
	}

	sectionAlign := binary.LittleEndian.Uint32(data[optOff+optSectionAlignment:])
	fileAlign := binary.LittleEndian.Uint32(data[optOff+optFileAlignment:])
	if !plausibleAlignment(fileAlign, maxFileAlignment) || !plausibleAlignment(sectionAlign, maxSectionAlignment) {
		return nil, fmt.Errorf("implausible alignment (section %#x, file %#x)", sectionAlign, fileAlign)
	}

	return &peImage{
		buf:           data,
		lfanew:        lfanew,
		coffOff:       coffOff,
		optOff:        optOff,
		optSize:       optSize,
		sectionsOff:   sectionsOff,
		numSections:   numSections,
		sectionAlign:  sectionAlign,
		fileAlign:     fileAlign,
		sizeOfHeaders: binary.LittleEndian.Uint32(data[optOff+optSizeOfHeaders:]),
	}, nil
}

type peSection struct {
	name          [8]byte
	virtualSize   uint32
	virtualAddr   uint32
	sizeOfRawData uint32
	pointerToRaw  uint32
}

func (p *peImage) section(i int) peSection {
	off := p.sectionsOff + i*sectionHeaderSize
	var s peSection
	copy(s.name[:], p.buf[off+shName:off+shName+shNameLen])
	s.virtualSize = binary.LittleEndian.Uint32(p.buf[off+shVirtualSize:])
	s.virtualAddr = binary.LittleEndian.Uint32(p.buf[off+shVirtualAddress:])
	s.sizeOfRawData = binary.LittleEndian.Uint32(p.buf[off+shSizeOfRawData:])
	s.pointerToRaw = binary.LittleEndian.Uint32(p.buf[off+shPointerToRawData:])
	return s
}

func alignUp(v, align uint32) uint32 {
	if align == 0 {
		return v
	}
	rem := v % align
	if rem == 0 {
		return v
	}
	return v + (align - rem)
}

// appendSections appends sections (in order) to the stub image data,
// returning a new, self-consistent PE32+ image. Each name must be at
// most 8 bytes.
func appendSections(data []byte, sections []namedSection) ([]byte, error) {
	pe, err := parsePE(data)
	if err != nil {
		return nil, fmt.Errorf("parsing stub: %w", err)
	}
	if len(sections) == 0 {
		return append([]byte(nil), data...), nil
	}
	for _, s := range sections {
		// A name starting with '/' is a PE string table reference
		// ("/4"), not a name.
		if strings.HasPrefix(s.Name, "/") {
			return nil, fmt.Errorf("section name %q starts with '/', which PE reserves for string table references", s.Name)
		}
		if len(s.Name) > 8 {
			return nil, fmt.Errorf("section name %q longer than 8 bytes", s.Name)
		}
		// PE32+ section sizes are 32-bit fields (shVirtualSize,
		// shSizeOfRawData): reject anything that wouldn't round-trip,
		// rather than silently truncating it into a corrupt image.
		if len(s.Data) > math.MaxUint32 {
			return nil, fmt.Errorf("section %q is %d bytes, too large for a PE32+ 32-bit size field", s.Name, len(s.Data))
		}
	}

	// pe.numSections came from the stub's own COFF header (a uint16) and
	// len(sections) is our own small, fixed list of UKI sections, so this
	// can't approach the int/uint32 boundary in practice.
	newTableEnd := pe.sectionsOff + (pe.numSections+len(sections))*sectionHeaderSize
	if uint32(newTableEnd) > pe.sizeOfHeaders { //nolint:gosec // G115: bounded by a uint16 section count plus our own fixed section list
		return nil, fmt.Errorf("stub has no room in its header area for %d more section headers (need offset %#x, SizeOfHeaders is %#x)",
			len(sections), newTableEnd, pe.sizeOfHeaders)
	}

	last := pe.section(pe.numSections - 1)
	nextVA := alignUp(last.virtualAddr+last.virtualSize, pe.sectionAlign)
	nextRaw := last.pointerToRaw + last.sizeOfRawData
	if pe.fileAlign != 0 && nextRaw%pe.fileAlign != 0 {
		return nil, fmt.Errorf("last section's raw data ends at %#x, which is not a multiple of the file alignment %#x", nextRaw, pe.fileAlign)
	}
	if int(nextRaw) != len(data) {
		// Not fatal in principle, but every stub we handle is expected
		// to end exactly at its last section's raw data; anything else
		// means our assumptions about this stub don't hold.
		return nil, fmt.Errorf("last section does not end at EOF (raw end %#x, file size %#x)", nextRaw, len(data))
	}

	out := append([]byte(nil), data...)

	var newHeaders []byte
	var newRaw []byte
	initializedDataAdd := uint32(0)
	for _, s := range sections {
		// len(s.Data) was checked above to fit uint32.
		rawSize := alignUp(uint32(len(s.Data)), pe.fileAlign) //nolint:gosec // G115: checked above
		var hdr [sectionHeaderSize]byte
		copy(hdr[shName:shName+shNameLen], []byte(s.Name))
		binary.LittleEndian.PutUint32(hdr[shVirtualSize:], uint32(len(s.Data))) //nolint:gosec // G115: checked above
		binary.LittleEndian.PutUint32(hdr[shVirtualAddress:], nextVA)
		binary.LittleEndian.PutUint32(hdr[shSizeOfRawData:], rawSize)
		binary.LittleEndian.PutUint32(hdr[shPointerToRawData:], nextRaw)
		binary.LittleEndian.PutUint32(hdr[shCharacteristics:], sectionCharacteristics)
		newHeaders = append(newHeaders, hdr[:]...)

		padded := make([]byte, rawSize)
		copy(padded, s.Data)
		newRaw = append(newRaw, padded...)

		initializedDataAdd += rawSize
		nextVA = alignUp(nextVA+uint32(len(s.Data)), pe.sectionAlign) //nolint:gosec // G115: checked above
		nextRaw += rawSize
	}

	copy(out[pe.sectionsOff+pe.numSections*sectionHeaderSize:], newHeaders)
	out = append(out, newRaw...)

	// pe.numSections (a uint16 from the stub's own COFF header) plus our
	// own small, fixed section list can't approach the uint16 boundary.
	binary.LittleEndian.PutUint16(out[pe.coffOff+coffNumberOfSections:], uint16(pe.numSections+len(sections))) //nolint:gosec // G115: bounded by a uint16 section count plus our own fixed section list

	sizeOfInit := binary.LittleEndian.Uint32(out[pe.optOff+optSizeOfInitializedData:])
	binary.LittleEndian.PutUint32(out[pe.optOff+optSizeOfInitializedData:], sizeOfInit+initializedDataAdd)

	binary.LittleEndian.PutUint32(out[pe.optOff+optSizeOfImage:], nextVA)

	// Zero the checksum: the field is advisory for a UEFI application and
	// recomputing it correctly requires re-scanning the whole image: easier
	// and just as valid to mark it "not set", which is what ukify does.
	binary.LittleEndian.PutUint32(out[pe.optOff+optCheckSum:], 0)

	return out, nil
}

type namedSection struct {
	Name string
	Data []byte
}
