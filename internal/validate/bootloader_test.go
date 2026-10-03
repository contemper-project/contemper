package validate_test

import (
	"archive/tar"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/validate"
)

const (
	machineAMD64 = 0x8664
	machineARM64 = 0xAA64
)

// synthPE builds a minimal PE image header: DOS stub, PE signature, COFF
// header and an optional header of the given magic and subsystem.
func synthPE(machine, magic, subsystem uint16) []byte {
	const peOff = 0x80
	b := make([]byte, peOff+24+240)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[0x3c:], peOff)
	copy(b[peOff:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[peOff+4:], machine)
	binary.LittleEndian.PutUint16(b[peOff+4+16:], 240)
	opt := b[peOff+24:]
	binary.LittleEndian.PutUint16(opt, magic)
	binary.LittleEndian.PutUint16(opt[68:], subsystem)
	return b
}

func goodPE(machine uint16) []byte { return synthPE(machine, 0x20b, 10) }

func TestCheckEFIApplication(t *testing.T) {
	trunc := goodPE(machineARM64)[:0x80+24+20]
	badSig := goodPE(machineARM64)
	badSig[0x80] = 'X'
	farOff := goodPE(machineARM64)
	binary.LittleEndian.PutUint32(farOff[0x3c:], 0xffffff)
	notMZ := goodPE(machineARM64)
	notMZ[0] = 'Z'

	for _, c := range []struct {
		name    string
		head    []byte
		machine uint16
		wantErr string
	}{
		{"arm64 ok", goodPE(machineARM64), machineARM64, ""},
		{"amd64 ok", goodPE(machineAMD64), machineAMD64, ""},
		{"wrong machine", goodPE(machineAMD64), machineARM64, "machine type is 0x8664"},
		{"PE32", synthPE(machineARM64, 0x10b, 10), machineARM64, "PE32 image"},
		{"unknown magic", synthPE(machineARM64, 0x107, 10), machineARM64, "magic"},
		{"boot service driver", synthPE(machineARM64, 0x20b, 11), machineARM64, "subsystem is 11"},
		{"windows gui", synthPE(machineARM64, 0x20b, 2), machineARM64, "subsystem is 2"},
		{"truncated after COFF", trunc, machineARM64, "truncated"},
		{"empty", nil, machineARM64, "MZ"},
		{"not MZ", notMZ, machineARM64, "MZ"},
		{"bad signature", badSig, machineARM64, "signature"},
		{"offset out of range", farOff, machineARM64, "out of range"},
		{"ascii", []byte(strings.Repeat("hello world ", 20)), machineARM64, "MZ"},
	} {
		err := validate.CheckEFIApplication(c.head, c.machine)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}
}

// signedPE returns goodPE with a Certificate Table data directory entry
// (offset, size) and a data directory count of numDirs.
func signedPE(machine uint16, numDirs, certOff, certSize uint32) []byte {
	b := goodPE(machine)
	opt := b[0x80+24:]
	binary.LittleEndian.PutUint32(opt[108:], numDirs)
	binary.LittleEndian.PutUint32(opt[112+4*8:], certOff)
	binary.LittleEndian.PutUint32(opt[112+4*8+4:], certSize)
	return b
}

func TestCheckSigned(t *testing.T) {
	const fileSize = 4096
	short := signedPE(machineARM64, 16, 3000, 500)
	binary.LittleEndian.PutUint16(short[0x80+4+16:], 112+4*8) // optional header ends before entry 4
	pe32 := signedPE(machineARM64, 16, 3000, 500)
	binary.LittleEndian.PutUint16(pe32[0x80+24:], 0x10b)
	for _, c := range []struct {
		name    string
		head    []byte
		wantErr string
	}{
		{"signed", signedPE(machineARM64, 16, 3000, 500), ""},
		{"signed to end of file", signedPE(machineAMD64, 16, 3596, 500), ""},
		{"unsigned", goodPE(machineARM64), "no Authenticode signature"},
		{"zero size", signedPE(machineARM64, 16, 3000, 0), "empty"},
		{"too few data directories", signedPE(machineARM64, 4, 3000, 500), "no certificate table"},
		{"header too short", short, "no certificate table"},
		{"zero offset", signedPE(machineARM64, 16, 0, 500), "not within"},
		{"beyond file", signedPE(machineARM64, 16, 3700, 500), "not within"},
		{"inside the headers", signedPE(machineARM64, 16, 200, 500), "inside the PE headers"},
		{"smaller than a header", signedPE(machineARM64, 16, 3000, 7), "WIN_CERTIFICATE"},
		{"PE32", pe32, "PE32+"},
		{"not PE", []byte(strings.Repeat("x", 300)), "MZ"},
	} {
		err := validate.CheckSigned(c.head, fileSize)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}
}

func TestCheckSecureBoot(t *testing.T) {
	signed := signedPE(machineARM64, 16, 0x200, 100)
	signed = append(signed, make([]byte, 0x300)...)
	files := espFiles()
	files[4].Data = signed
	rfs := build(t, files)
	res, err := validate.CheckBootloader(rfs, "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate.CheckSecureBoot(rfs, res.Bootloader); err != nil {
		t.Errorf("signed fallback: %v", err)
	}

	rfs = build(t, espFiles())
	res, err = validate.CheckBootloader(rfs, "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	err = validate.CheckSecureBoot(rfs, res.Bootloader)
	if err == nil || !strings.Contains(err.Error(), "BOOTAA64.EFI") || !strings.Contains(err.Error(), "no Authenticode signature") {
		t.Errorf("unsigned fallback: error = %v", err)
	}
}

func dir(p string) imgtest.File { return imgtest.File{Path: p, Typeflag: tar.TypeDir} }

// espFiles is a minimal valid /boot/efi tree for arm64.
func espFiles() []imgtest.File {
	return []imgtest.File{
		dir("boot/"), dir("boot/efi/"), dir("boot/efi/EFI/"), dir("boot/efi/EFI/BOOT/"),
		{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI", Data: goodPE(machineARM64)},
	}
}

const bigESP = 512 << 20

func TestCheckBootloaderMinimal(t *testing.T) {
	res, err := validate.CheckBootloader(build(t, espFiles()), "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Bootloader
	if b.FallbackPath != "EFI/BOOT/BOOTAA64.EFI" || b.FallbackSize != int64(len(goodPE(machineARM64))) {
		t.Errorf("fallback = %q, %d bytes", b.FallbackPath, b.FallbackSize)
	}
	if b.ContemperIgnored {
		t.Errorf("ContemperIgnored set without /boot/contemper")
	}
	var paths []string
	for _, f := range b.Files {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, ","); got != "EFI,EFI/BOOT,EFI/BOOT/BOOTAA64.EFI" {
		t.Errorf("files = %s", got)
	}
}

func TestCheckBootloaderAMD64(t *testing.T) {
	files := espFiles()
	files[4] = imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTX64.EFI", Data: goodPE(machineAMD64)}
	if _, err := validate.CheckBootloader(build(t, files), "amd64", bigESP); err != nil {
		t.Fatal(err)
	}
	// An arm64 fallback does not satisfy an amd64 target.
	if _, err := validate.CheckBootloader(build(t, espFiles()), "amd64", bigESP); err == nil {
		t.Fatal("expected an error: BOOTAA64.EFI is not the amd64 fallback file")
	}
	// An amd64 binary under the arm64 name is the wrong machine.
	files[4] = imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI", Data: goodPE(machineAMD64)}
	if _, err := validate.CheckBootloader(build(t, files), "arm64", bigESP); err == nil || !strings.Contains(err.Error(), "machine type") {
		t.Fatalf("error = %v, want a machine type error", err)
	}
}

func TestCheckBootloaderTree(t *testing.T) {
	with := func(extra ...imgtest.File) []imgtest.File { return append(espFiles(), extra...) }
	for _, c := range []struct {
		name    string
		files   []imgtest.File
		esp     uint64
		wantErr string // empty: must succeed
	}{
		{"nested dirs and files", with(dir("boot/efi/EFI/debian/"), imgtest.File{Path: "boot/efi/EFI/debian/grub.cfg", Data: []byte("x")},
			dir("boot/efi/loader/"), dir("boot/efi/loader/entries/"), imgtest.File{Path: "boot/efi/loader/entries/a-long-entry-name.conf", Data: []byte("x")}), bigESP, ""},
		{"symlink", with(imgtest.File{Path: "boot/efi/EFI/link", Typeflag: tar.TypeSymlink, Linkname: "BOOT"}), bigESP, "/boot/efi/EFI/link: a symlink"},
		{"char device", with(imgtest.File{Path: "boot/efi/dev", Typeflag: tar.TypeChar}), bigESP, "/boot/efi/dev: a character device"},
		{"fifo", with(imgtest.File{Path: "boot/efi/pipe", Typeflag: tar.TypeFifo}), bigESP, "/boot/efi/pipe: a FIFO"},
		{"hardlink inside", with(imgtest.File{Path: "boot/efi/EFI/BOOT/copy.efi", Typeflag: tar.TypeLink, Linkname: "boot/efi/EFI/BOOT/BOOTAA64.EFI"}), bigESP, ""},
		{"hardlink from outside", with(dir("boot/efi/EFI/x/"), imgtest.File{Path: "usr/lib/grub.efi", Data: []byte("g")},
			imgtest.File{Path: "boot/efi/EFI/x/grub.efi", Typeflag: tar.TypeLink, Linkname: "usr/lib/grub.efi"}), bigESP, ""},
		{"hardlink to symlink", with(imgtest.File{Path: "etc-link", Typeflag: tar.TypeSymlink, Linkname: "x"},
			imgtest.File{Path: "boot/efi/h", Typeflag: tar.TypeLink, Linkname: "etc-link"}), bigESP, "hardlink target"},
		{"invalid char", with(imgtest.File{Path: "boot/efi/a:b", Data: []byte("x")}), bigESP, "FAT"},
		{"control char", with(imgtest.File{Path: "boot/efi/a\x01b", Data: []byte("x")}), bigESP, "FAT"},
		{"trailing dot", with(imgtest.File{Path: "boot/efi/abc.", Data: []byte("x")}), bigESP, "ends in a space or dot"},
		{"name too long", with(imgtest.File{Path: "boot/efi/" + strings.Repeat("a", 256), Data: []byte("x")}), bigESP, "255"},
		{"max length name", with(imgtest.File{Path: "boot/efi/" + strings.Repeat("a", 255), Data: []byte("x")}), bigESP, ""},
		{"non-BMP", with(imgtest.File{Path: "boot/efi/\U0001F600", Data: []byte("x")}), bigESP, "printable ASCII"},
		{"non-ASCII", with(imgtest.File{Path: "boot/efi/café.txt", Data: []byte("x")}), bigESP, "printable ASCII"},
		{"non-ASCII BMP letter", with(imgtest.File{Path: "boot/efi/\u0141", Data: []byte("x")}), bigESP, "printable ASCII"},
		{"dot-leading", with(imgtest.File{Path: "boot/efi/.hidden", Data: []byte("x")}), bigESP, "starts with a dot"},
		{"spaces and punctuation", with(imgtest.File{Path: "boot/efi/My Boot+Stuff [1].conf", Data: []byte("x")}), bigESP, ""},
		{"case-only duplicate", with(imgtest.File{Path: "boot/efi/grub.cfg", Data: []byte("a")}, imgtest.File{Path: "boot/efi/GRUB.CFG", Data: []byte("b")}), bigESP, "same 8.3 short name"},
		{"short name clash", with(imgtest.File{Path: "boot/efi/a b", Data: []byte("a")}, imgtest.File{Path: "boot/efi/ab", Data: []byte("b")}), bigESP, "same 8.3 short name"},
		{"truncated extension clash", with(imgtest.File{Path: "boot/efi/foo.conf", Data: []byte("a")}, imgtest.File{Path: "boot/efi/foo.cons", Data: []byte("b")}), bigESP, "foo.cons"},
		{"kernel version clash", with(dir("boot/efi/m/"), imgtest.File{Path: "boot/efi/m/6.1.0-25-amd64", Data: []byte("a")}, imgtest.File{Path: "boot/efi/m/6.1.0-26-amd64", Data: []byte("b")}), bigESP, "map to the same 8.3 short name"},
		{"same name different dirs", with(dir("boot/efi/x/"), imgtest.File{Path: "boot/efi/x/ab", Data: []byte("a")}, imgtest.File{Path: "boot/efi/ab", Data: []byte("b")}), bigESP, ""},
		{"too large", with(imgtest.File{Path: "boot/efi/big", Data: make([]byte, 4096)}), 2048, "usable"},
	} {
		_, err := validate.CheckBootloader(build(t, c.files), "arm64", c.esp)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}
}

func TestCheckBootloaderHardlinkContent(t *testing.T) {
	files := []imgtest.File{
		dir("boot/"), dir("boot/efi/"), dir("boot/efi/EFI/"), dir("boot/efi/EFI/BOOT/"),
		{Path: "usr/share/bootaa64.efi", Data: goodPE(machineARM64)},
		{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI", Typeflag: tar.TypeLink, Linkname: "usr/share/bootaa64.efi"},
	}
	res, err := validate.CheckBootloader(build(t, files), "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	last := res.Bootloader.Files[len(res.Bootloader.Files)-1]
	if last.Source == nil || last.Source.Path != "/usr/share/bootaa64.efi" || last.Size != int64(len(goodPE(machineARM64))) {
		t.Errorf("hardlink source = %+v", last)
	}
}

func TestCheckBootloaderTooLargeNamesBothSizes(t *testing.T) {
	files := append(espFiles(), imgtest.File{Path: "boot/efi/big", Data: make([]byte, 3<<20)})
	_, err := validate.CheckBootloader(build(t, files), "arm64", 2<<20)
	if err == nil || !strings.Contains(err.Error(), "3.0 MiB") || !strings.Contains(err.Error(), "2.0 MiB") {
		t.Fatalf("error = %v, want both the tree size and the ESP size", err)
	}
}

func TestCheckBootloaderFallbackCaseInsensitive(t *testing.T) {
	files := []imgtest.File{
		dir("boot/"), dir("boot/efi/"), dir("boot/efi/efi/"), dir("boot/efi/efi/boot/"),
		{Path: "boot/efi/efi/boot/bootaa64.efi", Data: goodPE(machineARM64)},
	}
	res, err := validate.CheckBootloader(build(t, files), "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bootloader.FallbackPath != "efi/boot/bootaa64.efi" {
		t.Errorf("FallbackPath = %q, want the path as found in the image", res.Bootloader.FallbackPath)
	}
	// The PE check applies to the file found under the other case.
	files[4].Data = []byte("not a PE image")
	if _, err := validate.CheckBootloader(build(t, files), "arm64", bigESP); err == nil || !strings.Contains(err.Error(), "not a PE image") {
		t.Errorf("error = %v, want a PE error for the lower-case fallback file", err)
	}
}

func TestCheckBootloaderRealisticTrees(t *testing.T) {
	pe := goodPE(machineAMD64)
	grub := []imgtest.File{dir("boot/"), dir("boot/efi/"), dir("boot/efi/EFI/"), dir("boot/efi/EFI/debian/"), dir("boot/efi/EFI/BOOT/")}
	for _, n := range []string{"grubx64.efi", "shimx64.efi", "mmx64.efi", "fbx64.efi", "BOOTX64.CSV", "grub.cfg"} {
		grub = append(grub, imgtest.File{Path: "boot/efi/EFI/debian/" + n, Data: []byte(n)})
	}
	grub = append(grub,
		imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTX64.EFI", Data: pe},
		imgtest.File{Path: "boot/efi/EFI/BOOT/fbx64.efi", Data: []byte("fb")},
		imgtest.File{Path: "boot/efi/EFI/BOOT/mmx64.efi", Data: []byte("mm")})
	if _, err := validate.CheckBootloader(build(t, grub), "amd64", bigESP); err != nil {
		t.Errorf("Debian GRUB with shim: %v", err)
	}

	const id = "0123456789abcdef0123456789abcdef"
	sdb := []imgtest.File{
		dir("boot/"), dir("boot/efi/"), dir("boot/efi/EFI/"), dir("boot/efi/EFI/BOOT/"),
		{Path: "boot/efi/EFI/BOOT/BOOTX64.EFI", Data: pe},
		dir("boot/efi/loader/"), dir("boot/efi/loader/entries/"),
		{Path: "boot/efi/loader/loader.conf", Data: []byte("x")},
		{Path: "boot/efi/loader/entries/" + id + "-6.12.1-amd64.conf", Data: []byte("a")},
		{Path: "boot/efi/loader/entries/" + id + "-6.12.2-amd64.conf", Data: []byte("b")},
	}
	if _, err := validate.CheckBootloader(build(t, sdb), "amd64", bigESP); err != nil {
		t.Errorf("systemd-boot entries: %v", err)
	}
}

func TestCheckBootloaderFATOverhead(t *testing.T) {
	const esp = 4 << 20
	cluster, usable := disk.ESPGeometry(esp)
	// Root, EFI and BOOT directories and the fallback file take one cluster
	// each; the extra file's directory entry fits in the root's cluster.
	room := int(usable - 4*cluster)
	with := func(size int) []imgtest.File {
		return append(espFiles(), imgtest.File{Path: "boot/efi/big", Data: make([]byte, size)})
	}
	if _, err := validate.CheckBootloader(build(t, with(room)), "arm64", esp); err != nil {
		t.Errorf("a tree that exactly fills the usable area: %v", err)
	}
	if _, err := validate.CheckBootloader(build(t, with(room+1)), "arm64", esp); err == nil || !strings.Contains(err.Error(), "usable") {
		t.Errorf("one byte more: error = %v, want it not to fit", err)
	}
	// Many tiny files each take a whole cluster, though their sizes sum
	// to almost nothing.
	files := espFiles()
	for i := range int(usable/cluster) + 1 {
		files = append(files, imgtest.File{Path: fmt.Sprintf("boot/efi/f%d", i), Data: []byte("x")})
	}
	if _, err := validate.CheckBootloader(build(t, files), "arm64", esp); err == nil {
		t.Errorf("tiny files rounded up to clusters should not fit")
	}
}

func TestCheckBootloaderFallback(t *testing.T) {
	skel := []imgtest.File{dir("boot/"), dir("boot/efi/"), dir("boot/efi/EFI/"), dir("boot/efi/EFI/BOOT/")}
	for _, c := range []struct {
		name    string
		files   []imgtest.File
		wantErr string
	}{
		{"missing /boot/efi", []imgtest.File{dir("boot/")}, "/boot/efi"},
		{"/boot/efi is a file", []imgtest.File{dir("boot/"), {Path: "boot/efi", Data: []byte("x")}}, "not a directory"},
		{"missing fallback", skel, "BOOTAA64.EFI is missing"},
		{"fallback in wrong dir", append(append([]imgtest.File{}, skel...), imgtest.File{Path: "boot/efi/EFI/BOOTAA64.EFI", Data: goodPE(machineARM64)}), "missing"},
		{"fallback is a directory", append(append([]imgtest.File{}, skel...), dir("boot/efi/EFI/BOOT/BOOTAA64.EFI/")), "is a directory"},
		{"fallback is a symlink", append(append([]imgtest.File{}, skel...), imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI", Typeflag: tar.TypeSymlink, Linkname: "grub.efi"}), "symlink"},
		{"fallback not PE", append(append([]imgtest.File{}, skel...), imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI", Data: []byte("#!/bin/sh\n")}), "not a PE image"},
		{"fallback empty", append(append([]imgtest.File{}, skel...), imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI"}), "not a PE image"},
		{"fallback PE32", append(append([]imgtest.File{}, skel...), imgtest.File{Path: "boot/efi/EFI/BOOT/BOOTAA64.EFI", Data: synthPE(machineARM64, 0x10b, 10)}), "PE32"},
	} {
		_, err := validate.CheckBootloader(build(t, c.files), "arm64", bigESP)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.wantErr)
		}
	}
}

func TestCheckBootloaderEFIDirSymlink(t *testing.T) {
	files := []imgtest.File{
		dir("boot/"), dir("usr/"), dir("usr/lib/"), dir("usr/lib/efi/"), dir("usr/lib/efi/EFI/"), dir("usr/lib/efi/EFI/BOOT/"),
		{Path: "usr/lib/efi/EFI/BOOT/BOOTAA64.EFI", Data: goodPE(machineARM64)},
		{Path: "boot/efi", Typeflag: tar.TypeSymlink, Linkname: "../usr/lib/efi"},
	}
	res, err := validate.CheckBootloader(build(t, files), "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bootloader.EFIRoot != "/usr/lib/efi" {
		t.Errorf("EFIRoot = %q", res.Bootloader.EFIRoot)
	}
}

func TestCheckBootloaderContemperIgnored(t *testing.T) {
	files := append(espFiles(), dir("boot/contemper/"), imgtest.File{Path: "boot/contemper/vmlinuz", Data: []byte("k")})
	res, err := validate.CheckBootloader(build(t, files), "arm64", bigESP)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Bootloader.ContemperIgnored {
		t.Errorf("ContemperIgnored not set although /boot/contemper exists")
	}
}

func TestCheckBootloaderUnknownArch(t *testing.T) {
	if _, err := validate.CheckBootloader(build(t, espFiles()), "riscv64", bigESP); err == nil {
		t.Fatal("expected an error for an architecture without a fallback file")
	}
}

func TestCheckBootloaderInitMissing(t *testing.T) {
	res, err := validate.CheckBootloader(build(t, espFiles()), "arm64", bigESP)
	if err != nil {
		t.Fatalf("a bootloader image without /sbin/init is valid: %v", err)
	}
	if !res.Bootloader.InitMissing {
		t.Errorf("InitMissing not set without /sbin/init")
	}
	files := append(espFiles(), dir("sbin/"), imgtest.File{Path: "sbin/init", Data: []byte("x"), Mode: 0o755})
	res, err = validate.CheckBootloader(build(t, files), "arm64", bigESP)
	if err != nil || res.Bootloader.InitMissing {
		t.Errorf("with /sbin/init: InitMissing = %v, err = %v", res != nil && res.Bootloader.InitMissing, err)
	}
}
