package main

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/contemper-project/contemper/internal/bundle"
	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/progress"
	"github.com/contemper-project/contemper/internal/validate"
)

// efiApp returns the headers of a minimal EFI application PE32+ image for
// the host architecture, and the fallback file name for it.
func efiApp() (data []byte, fallback string) {
	machine, fallback := uint16(0xAA64), "BOOTAA64.EFI"
	if runtime.GOARCH == "amd64" {
		machine, fallback = 0x8664, "BOOTX64.EFI"
	}
	b := make([]byte, 0x80+24+240)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	copy(b[0x80:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[0x84:], machine)
	binary.LittleEndian.PutUint16(b[0x84+16:], 240)
	binary.LittleEndian.PutUint16(b[0x98:], 0x20b)
	binary.LittleEndian.PutUint16(b[0x98+68:], 10)
	return b, fallback
}

func bootloaderFixtureFiles() []imgtest.File {
	pe, fallback := efiApp()
	return []imgtest.File{
		{Path: "boot/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/BOOT/", Typeflag: tar.TypeDir},
		{Path: "boot/efi/EFI/BOOT/" + fallback, Data: pe},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
	}
}

// convertFixture converts an image with the given labels and files in
// process, with a fake qemu-img, and returns the bundle directory (empty
// on failure) and the error.
func convertFixture(t *testing.T, labels map[string]string, files []imgtest.File) (bundleDir string, err error) {
	t.Helper()
	return convertFixtureWith(t, labels, files, func(*convertOptions) {})
}

// convertFixtureWith is convertFixture with a hook to adjust the options.
func convertFixtureWith(t *testing.T, labels map[string]string, files []imgtest.File, adjust func(*convertOptions)) (bundleDir string, err error) {
	t.Helper()
	return convertFixtureVolumes(t, labels, files, nil, adjust)
}

// convertFixtureVolumes is convertFixtureWith for an image that declares
// the given VOLUME paths.
func convertFixtureVolumes(t *testing.T, labels map[string]string, files []imgtest.File, volumes []string, adjust func(*convertOptions)) (bundleDir string, err error) {
	t.Helper()
	requireExt4HostTools(t)
	archive := buildArchiveWithVolumes(t, labels, files, volumes)
	outDir := t.TempDir()
	t.Setenv("PATH", installFakeTool(t, "qemu-img", fastQemuImgScript)+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	opts := convertOptions{
		sourceRef:    "oci-archive:" + archive,
		target:       "qemu",
		outDir:       outDir,
		progressMode: "plain",
	}
	adjust(&opts)
	err = runConvert(t.Context(), cmd, opts)
	entries, _ := os.ReadDir(outDir)
	if len(entries) == 1 {
		bundleDir = filepath.Join(outDir, entries[0].Name())
	}
	return bundleDir, err
}

func TestConvertBootloaderImageRecordsMode(t *testing.T) {
	dir, err := convertFixture(t, map[string]string{
		"io.contemper.ready": "true",
		"io.contemper.boot":  "bootloader",
	}, bootloaderFixtureFiles())
	if err != nil {
		t.Fatal(err)
	}
	m, err := bundle.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Boot != "bootloader" {
		t.Errorf("manifest boot = %q, want bootloader", m.Boot)
	}
}

func TestConvertUKIImageRecordsMode(t *testing.T) {
	for name, labels := range map[string]map[string]string{
		"no label":       {"io.contemper.ready": "true"},
		"explicit label": {"io.contemper.ready": "true", "io.contemper.boot": "uki"},
	} {
		dir, err := convertFixture(t, labels, ukiFixtureFiles)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		m, err := bundle.Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if m.Boot != "uki" {
			t.Errorf("%s: manifest boot = %q, want uki", name, m.Boot)
		}
	}
}

func TestConvertRejectsBadBoot(t *testing.T) {
	noFallback := bootloaderFixtureFiles()[:4]
	for _, c := range []struct {
		name    string
		labels  map[string]string
		files   []imgtest.File
		wantErr string
	}{
		{"unknown value", map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "grub"}, ukiFixtureFiles, "io.contemper.boot"},
		{"no fallback file", map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "bootloader"}, noFallback, "is missing"},
		{"bootloader label without /boot/efi", map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "bootloader"}, ukiFixtureFiles, "/boot/efi"},
	} {
		dir, err := convertFixture(t, c.labels, c.files)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: error = %v, want it to contain %q", c.name, err, c.wantErr)
		}
		if dir != "" {
			t.Errorf("%s: a bundle was written despite the error", c.name)
		}
	}
}

func TestConvertBootloaderWithoutSbinInit(t *testing.T) {
	// /sbin/init is not required in this mode: the bootloader
	// configuration can pass init=.
	files := bootloaderFixtureFiles()[:5]
	dir, err := convertFixture(t, map[string]string{
		"io.contemper.ready": "true",
		"io.contemper.boot":  "bootloader",
	}, files)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := bundle.Read(dir); err != nil || m.Boot != "bootloader" {
		t.Errorf("manifest = %+v, err = %v", m, err)
	}
}

func TestConvertBootloaderBootDirIsSymlink(t *testing.T) {
	pe, fallback := efiApp()
	files := []imgtest.File{
		{Path: "usr/", Typeflag: tar.TypeDir},
		{Path: "usr/lib/", Typeflag: tar.TypeDir},
		{Path: "usr/lib/boot/", Typeflag: tar.TypeDir},
		{Path: "usr/lib/boot/efi/", Typeflag: tar.TypeDir},
		{Path: "usr/lib/boot/efi/EFI/", Typeflag: tar.TypeDir},
		{Path: "usr/lib/boot/efi/EFI/BOOT/", Typeflag: tar.TypeDir},
		{Path: "usr/lib/boot/efi/EFI/BOOT/" + fallback, Data: pe},
		{Path: "boot", Typeflag: tar.TypeSymlink, Linkname: "usr/lib/boot"},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
	}
	dir, err := convertFixture(t, map[string]string{
		"io.contemper.ready": "true",
		"io.contemper.boot":  "bootloader",
	}, files)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := bundle.Read(dir); err != nil || m.Boot != "bootloader" {
		t.Errorf("manifest = %+v, err = %v", m, err)
	}
}

func reportText(f func(*progress.Reporter)) string {
	var buf bytes.Buffer
	f(progress.New(&buf, progress.ModePlain, false, false))
	return buf.String()
}

func TestReportBootloader(t *testing.T) {
	b := &validate.Bootloader{
		EFIRoot:      "/usr/lib/efi",
		Files:        []validate.ESPFile{{Path: "EFI", Dir: true}, {Path: "EFI/BOOT/BOOTX64.EFI", Size: 1024}},
		TotalBytes:   1024,
		FallbackPath: "EFI/BOOT/BOOTX64.EFI",
		FallbackSize: 1024,
	}
	out := reportText(func(rep *progress.Reporter) { reportBootloader(rep, b) })
	for _, want := range []string{"/boot/efi → /usr/lib/efi", "1 file", " EFI/BOOT/BOOTX64.EFI", "EFI application"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/EFI/BOOT") || strings.Contains(out, "ignored") || strings.Contains(out, "init=") {
		t.Errorf("report has lines it should not:\n%s", out)
	}

	b.ContemperIgnored, b.InitMissing = true, true
	out = reportText(func(rep *progress.Reporter) { reportBootloader(rep, b) })
	for _, want := range []string{"/boot/contemper", "ignored in bootloader mode", "/sbin/init", "init="} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// fstabInBundle converts the image with --keep-raw and returns the
// /etc/fstab of the root filesystem in the resulting disk.raw ("" if the
// image has none), read with debugfs from the second partition.
func fstabInBundle(t *testing.T, labels map[string]string, files []imgtest.File, adjust func(*convertOptions)) string {
	t.Helper()
	dir, err := convertFixtureWith(t, labels, files, func(o *convertOptions) {
		o.keepRaw = true
		if adjust != nil {
			adjust(o)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.Open(filepath.Join(dir, "disk.raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	// GPT: header at LBA 1, entries from the LBA it names; entry 2 (128
	// bytes each) holds the root partition's first and last LBA.
	hdr := make([]byte, 92)
	if _, err := raw.ReadAt(hdr, 512); err != nil {
		t.Fatal(err)
	}
	entries := int64(binary.LittleEndian.Uint64(hdr[72:]))
	ent := make([]byte, 128)
	if _, err := raw.ReadAt(ent, entries*512+128); err != nil {
		t.Fatal(err)
	}
	first, last := int64(binary.LittleEndian.Uint64(ent[32:])), int64(binary.LittleEndian.Uint64(ent[40:]))
	part := filepath.Join(t.TempDir(), "root.img")
	out, err := os.Create(part)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, io.NewSectionReader(raw, first*512, (last-first+1)*512)); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := exec.CommandContext(t.Context(), hostenv.Find("debugfs"), "-R", "cat /etc/fstab", part).Output()
	if err != nil {
		t.Fatalf("debugfs: %v", err)
	}
	return string(got)
}

const espLine = "LABEL=ESP /boot/efi vfat umask=0077 0 2"

func TestConvertBootloaderMountsESP(t *testing.T) {
	bootLabels := map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "bootloader"}
	withFstab := func(content string) []imgtest.File {
		return append(bootloaderFixtureFiles(),
			imgtest.File{Path: "etc/", Typeflag: tar.TypeDir},
			imgtest.File{Path: "etc/fstab", Data: []byte(content)})
	}

	if got := fstabInBundle(t, bootLabels, withFstab("/dev/vda2 / ext4 defaults 0 1\n"), nil); got != "/dev/vda2 / ext4 defaults 0 1\n"+espLine+"\n" {
		t.Errorf("appended: fstab = %q", got)
	}
	if got := fstabInBundle(t, bootLabels, bootloaderFixtureFiles(), nil); got != espLine+"\n" {
		t.Errorf("no image fstab: fstab = %q", got)
	}
	own := "# esp\nUUID=1234-ABCD /boot/efi vfat defaults 0 2\n"
	if got := fstabInBundle(t, bootLabels, withFstab(own), nil); got != own {
		t.Errorf("image's own entry: fstab = %q, want it untouched", got)
	}
	optOut := map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "bootloader", "io.contemper.fstab": "false"}
	if got := fstabInBundle(t, optOut, withFstab("x\n"), nil); strings.Contains(got, "/boot/efi") {
		t.Errorf("label opt-out: fstab = %q", got)
	}
	if got := fstabInBundle(t, bootLabels, withFstab("x\n"), func(o *convertOptions) { o.noFstab = true }); strings.Contains(got, "/boot/efi") {
		t.Errorf("--no-fstab: fstab = %q", got)
	}
}

func TestConvertBootloaderESPBehindSymlink(t *testing.T) {
	bootLabels := map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "bootloader"}
	pe, fallback := efiApp()
	files := []imgtest.File{
		{Path: "boot/", Typeflag: tar.TypeDir},
		{Path: "boot/efi", Typeflag: tar.TypeSymlink, Linkname: "/efi"},
		{Path: "efi/", Typeflag: tar.TypeDir},
		{Path: "efi/EFI/", Typeflag: tar.TypeDir},
		{Path: "efi/EFI/BOOT/", Typeflag: tar.TypeDir},
		{Path: "efi/EFI/BOOT/" + fallback, Data: pe},
		{Path: "sbin/", Typeflag: tar.TypeDir},
		{Path: "sbin/init", Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: "etc/", Typeflag: tar.TypeDir},
		{Path: "etc/fstab", Data: []byte("UUID=1234-ABCD /efi vfat defaults 0 2\n")},
	}
	if got := fstabInBundle(t, bootLabels, files, nil); got != "UUID=1234-ABCD /efi vfat defaults 0 2\n" {
		t.Errorf("entry for the resolved directory: fstab = %q, want it untouched", got)
	}
}

func TestConvertBootloaderRejectsVolumeOverESP(t *testing.T) {
	bootLabels := map[string]string{"io.contemper.ready": "true", "io.contemper.boot": "bootloader"}
	for _, vol := range []string{"/boot/efi", "/boot", "/"} {
		_, err := convertFixtureVolumes(t, bootLabels, bootloaderFixtureFiles(), []string{vol}, func(o *convertOptions) { o.noVolHelper = true })
		if err == nil || !strings.Contains(err.Error(), "ESP mount point") {
			t.Errorf("volume %s: err = %v, want an ESP mount point error", vol, err)
		}
	}
	if _, err := convertFixtureVolumes(t, bootLabels, bootloaderFixtureFiles(), []string{"/data"}, func(o *convertOptions) { o.noVolHelper = true }); err != nil {
		t.Errorf("volume /data: %v", err)
	}
}

func TestConvertUKIAddsNoESPLine(t *testing.T) {
	files := append(append([]imgtest.File{}, ukiFixtureFiles...),
		imgtest.File{Path: "etc/", Typeflag: tar.TypeDir},
		imgtest.File{Path: "etc/fstab", Data: []byte("x\n")})
	if got := fstabInBundle(t, map[string]string{"io.contemper.ready": "true"}, files, nil); got != "x\n" {
		t.Errorf("UKI fstab = %q, want it unchanged", got)
	}
}
