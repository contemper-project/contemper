package validate

import (
	"encoding/binary"
	"strings"
	"testing"
)

// peHead builds a PE32+ header block of the given machine and subsystem
// with room for 16 data directories, the certificate table entry
// pointing at certOff/certSize.
func peHead(machine, subsystem uint16, certOff, certSize uint32) []byte {
	const peOff = 0x80
	b := make([]byte, peOff+24+240)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[0x3c:], peOff)
	copy(b[peOff:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[peOff+4:], machine)
	binary.LittleEndian.PutUint16(b[peOff+4+16:], 240)
	opt := b[peOff+24:]
	binary.LittleEndian.PutUint16(opt, peOptMagic32Plus)
	binary.LittleEndian.PutUint16(opt[peSubsystemOffset:], subsystem)
	binary.LittleEndian.PutUint32(opt[peNumRvaAndSizesOffset:], 16)
	entry := peDataDirsOffset + peCertTableIndex*8
	binary.LittleEndian.PutUint32(opt[entry:], certOff)
	binary.LittleEndian.PutUint32(opt[entry+4:], certSize)
	return b
}

// FuzzPEChecks runs both PE header checks over arbitrary bytes. They
// must not panic, must agree with each other, and a head they accept has
// to have the fields they claim to have checked.
func FuzzPEChecks(f *testing.F) {
	const amd64, arm64 = 0x8664, 0xAA64
	f.Add([]byte{}, uint16(amd64), int64(0))
	f.Add([]byte("MZ"), uint16(arm64), int64(2))
	f.Add(peHead(amd64, 10, 0, 0), uint16(amd64), int64(1000))
	f.Add(peHead(arm64, 10, 0x1000, 0x40), uint16(arm64), int64(0x1040))
	f.Add(peHead(arm64, 10, 0x1000, 0x40), uint16(amd64), int64(0x1040))
	f.Add(peHead(amd64, 3, 0x1000, 4), uint16(amd64), int64(0x2000))
	f.Add(peHead(amd64, 10, 0xFFFFFFF0, 0xFFFFFFF0), uint16(amd64), int64(1)<<40)
	f.Add(peHead(amd64, 10, 0x1000, 0x40)[:0x80+24+20], uint16(amd64), int64(0x1040))
	f.Fuzz(func(t *testing.T, head []byte, machine uint16, fileSize int64) {
		efiErr := CheckEFIApplication(head, machine)
		signedErr := CheckSigned(head, fileSize)

		// Extending the head with more file content never turns an
		// accepted header into a rejected one.
		if efiErr == nil {
			if err := CheckEFIApplication(append(append([]byte(nil), head...), make([]byte, 16)...), machine); err != nil {
				t.Fatalf("CheckEFIApplication accepted head but not head + 16 bytes: %v", err)
			}
			if len(head) < 0x40 || head[0] != 'M' || head[1] != 'Z' {
				t.Fatalf("accepted a head without an MZ header")
			}
			off := int(binary.LittleEndian.Uint32(head[0x3c:]))
			opt := head[off+24:]
			if string(head[off:off+4]) != "PE\x00\x00" ||
				binary.LittleEndian.Uint16(head[off+4:]) != machine ||
				binary.LittleEndian.Uint16(opt) != peOptMagic32Plus ||
				binary.LittleEndian.Uint16(opt[peSubsystemOffset:]) != peSubsystemEFIApplication {
				t.Fatalf("CheckEFIApplication accepted a head that is not a %#x EFI application PE32+", machine)
			}
			// A head that is an EFI application is a PE32+ image, so
			// CheckSigned never rejects it as "not a PE".
			if signedErr != nil && strings.HasPrefix(signedErr.Error(), "not a PE") {
				t.Fatalf("CheckSigned says %q for a head CheckEFIApplication accepts", signedErr)
			}
		}

		if signedErr == nil {
			off := int(binary.LittleEndian.Uint32(head[0x3c:]))
			optSize := int64(binary.LittleEndian.Uint16(head[off+4+16:]))
			opt := head[off+24:]
			entry := peDataDirsOffset + peCertTableIndex*8
			certOff := int64(binary.LittleEndian.Uint32(opt[entry:]))
			certSize := int64(binary.LittleEndian.Uint32(opt[entry+4:]))
			if certSize < peWinCertHeaderSize || certOff+certSize > fileSize || certOff < int64(off)+24+optSize {
				t.Fatalf("CheckSigned accepted certificate table %d+%d in a %d-byte file with headers ending at %d",
					certOff, certSize, fileSize, int64(off)+24+optSize)
			}
		}
	})
}

// isValidFATName restates checkFATName's documented rules independently.
func isValidFATName(name string) bool {
	if name == "" || len(name) > 255 || name[0] == '.' {
		return false
	}
	if last := name[len(name)-1]; last == ' ' || last == '.' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c > 0x7e || strings.IndexByte(`\/:*?"<>|`, c) >= 0 {
			return false
		}
	}
	return true
}

// FuzzFATNames checks checkFATName against an independent statement of
// its rules, and shortNameKey's folding: names differing only in case
// share a key, and a short-stem key is always a plain 8.3 name.
func FuzzFATNames(f *testing.F) {
	for _, s := range []string{
		"", ".", "..", ".hidden", "BOOTX64.EFI", "grub.cfg", "a b", "ab", "foo.conf", "foo.cons",
		"6.1.0-25-amd64", "6.1.0-26-amd64", "trailing ", "trailing.", "dir/name", "col:on",
		"tab\tname", "\x7f", "ünï", strings.Repeat("a", 255), strings.Repeat("a", 256),
		"verylongname.extension", "UPPER.CASE", "a.b.c", "x.", "...", " ",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		err := checkFATName(name)
		if (err == nil) != isValidFATName(name) {
			t.Fatalf("checkFATName(%q) = %v, but the rules say valid = %v", name, err, isValidFATName(name))
		}
		if err != nil {
			return
		}
		key := shortNameKey(name)
		if shortNameKey(strings.ToUpper(name)) != key || shortNameKey(strings.ToLower(name)) != key {
			t.Fatalf("shortNameKey is not case-insensitive for %q: %q, %q, %q",
				name, key, shortNameKey(strings.ToUpper(name)), shortNameKey(strings.ToLower(name)))
		}
		if long, ok := strings.CutPrefix(key, "L:"); ok {
			if long != strings.ToUpper(name) {
				t.Fatalf("long key %q for %q", key, name)
			}
			return
		}
		stem, ext, ok := strings.Cut(key, ".")
		if !ok || len(stem) > 8 || len(ext) > 3 || strings.Contains(ext, ".") {
			t.Fatalf("short key %q for %q is not 8.3", key, name)
		}
		for _, c := range key {
			if c != '.' && !strings.ContainsRune("!#$%&'()-0123456789@ABCDEFGHIJKLMNOPQRSTUVWXYZ^_`{}~", c) {
				t.Fatalf("short key %q for %q has %q", key, name, c)
			}
		}
	})
}
