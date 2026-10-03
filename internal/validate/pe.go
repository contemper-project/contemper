package validate

import (
	"encoding/binary"
	"fmt"
)

const (
	peOptMagic32Plus = 0x20b
	peOptMagic32     = 0x10b
	// peSubsystemEFIApplication is the optional-header subsystem value of
	// an EFI application.
	peSubsystemEFIApplication = 10
	// peSubsystemOffset is the subsystem field's offset in the optional
	// header, the same for PE32 and PE32+.
	peSubsystemOffset = 68
)

// CheckEFIApplication reports whether head, the first bytes of a file,
// holds the headers of a PE32+ image for the COFF machine type machine
// whose subsystem is EFI application. It only reads the headers.
func CheckEFIApplication(head []byte, machine uint16) error {
	if len(head) < 0x40 || head[0] != 'M' || head[1] != 'Z' {
		return fmt.Errorf("not a PE image (no MZ header)")
	}
	off := int(binary.LittleEndian.Uint32(head[0x3c:]))
	// PE signature (4) + COFF header (20) must fit.
	if off < 0 || off > len(head)-24 {
		return fmt.Errorf("not a PE image (PE header offset %#x is out of range)", off)
	}
	if string(head[off:off+4]) != "PE\x00\x00" {
		return fmt.Errorf("not a PE image (no PE signature)")
	}
	coff := head[off+4:]
	got := binary.LittleEndian.Uint16(coff)
	optSize := int(binary.LittleEndian.Uint16(coff[16:]))
	opt := head[off+24:]
	if optSize < peSubsystemOffset+2 || len(opt) < peSubsystemOffset+2 {
		return fmt.Errorf("not an EFI application (truncated or missing PE optional header)")
	}
	switch magic := binary.LittleEndian.Uint16(opt); magic {
	case peOptMagic32Plus:
	case peOptMagic32:
		return fmt.Errorf("a PE32 image, but PE32+ is required")
	default:
		return fmt.Errorf("unknown PE optional header magic %#x, PE32+ is required", magic)
	}
	if got != machine {
		return fmt.Errorf("COFF machine type is %#x, but the target architecture needs %#x", got, machine)
	}
	if sub := binary.LittleEndian.Uint16(opt[peSubsystemOffset:]); sub != peSubsystemEFIApplication {
		return fmt.Errorf("PE subsystem is %d, but an EFI application (%d) is required", sub, peSubsystemEFIApplication)
	}
	return nil
}

const (
	// peNumRvaAndSizesOffset is the offset of the data directory count in
	// a PE32+ optional header.
	peNumRvaAndSizesOffset = 108
	// peDataDirsOffset is where the data directories start in a PE32+
	// optional header.
	peDataDirsOffset = 112
	// peCertTableIndex is the Certificate Table's index among the data
	// directories.
	peCertTableIndex = 4
	// peWinCertHeaderSize is the size of a WIN_CERTIFICATE header, the
	// smallest possible certificate table entry.
	peWinCertHeaderSize = 8
)

// CheckSigned reports whether head, the first bytes of a PE32+ file of
// fileSize bytes, carries an Authenticode signature: the optional
// header's Certificate Table data directory has a a size of at
// least one WIN_CERTIFICATE header, and lies within the file, after the
// PE headers. This is only a sanity check that catches an
// unsigned binary early. It does not parse the signature or verify its
// chain, so it says nothing about whether firmware will trust it.
func CheckSigned(head []byte, fileSize int64) error {
	if len(head) < 0x40 || head[0] != 'M' || head[1] != 'Z' {
		return fmt.Errorf("not a PE image (no MZ header)")
	}
	off := int(binary.LittleEndian.Uint32(head[0x3c:]))
	if off < 0 || off > len(head)-24 || string(head[off:off+4]) != "PE\x00\x00" {
		return fmt.Errorf("not a PE image (no PE signature)")
	}
	optSize := int(binary.LittleEndian.Uint16(head[off+4+16:]))
	opt := head[off+24:]
	if len(opt) < 2 || binary.LittleEndian.Uint16(opt) != peOptMagic32Plus {
		return fmt.Errorf("not a PE32+ image")
	}
	entry := peDataDirsOffset + peCertTableIndex*8
	if optSize < entry+8 || len(opt) < entry+8 || len(opt) < peNumRvaAndSizesOffset+4 ||
		binary.LittleEndian.Uint32(opt[peNumRvaAndSizesOffset:]) <= peCertTableIndex {
		return fmt.Errorf("no Authenticode signature (the PE header has no certificate table)")
	}
	certOff := int64(binary.LittleEndian.Uint32(opt[entry:]))
	certSize := int64(binary.LittleEndian.Uint32(opt[entry+4:]))
	if certSize == 0 {
		return fmt.Errorf("no Authenticode signature (the certificate table is empty)")
	}
	if certOff == 0 || certOff+certSize > fileSize {
		return fmt.Errorf("the certificate table (offset %d, size %d) is not within the %d-byte file", certOff, certSize, fileSize)
	}
	if headersEnd := int64(off + 24 + optSize); certOff < headersEnd {
		return fmt.Errorf("the certificate table (offset %d) starts inside the PE headers, which end at %d", certOff, headersEnd)
	}
	if certSize < peWinCertHeaderSize {
		return fmt.Errorf("the certificate table (size %d) is smaller than a %d-byte WIN_CERTIFICATE header", certSize, peWinCertHeaderSize)
	}
	return nil
}
