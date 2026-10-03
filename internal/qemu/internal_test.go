package qemu

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildArgsWithFirmware(t *testing.T) {
	pflash := writeFakeFile(t, "edk2-fake.fd")
	info := archInfo{
		machine:          "virt",
		pflashCandidates: []firmware{{code: pflash}},
		biosCandidates:   nil,
	}
	args, err := buildArgs(info, Options{
		DiskPath:      "/tmp/bundle/disk.qcow2",
		DiskFormat:    "qcow2",
		SerialLogPath: "/tmp/serial.log",
	})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-M virt",
		"if=pflash,format=raw,unit=0,readonly=on,file=" + pflash,
		"file=/tmp/bundle/disk.qcow2,if=virtio,format=qcow2,snapshot=on",
		"-nographic",
		"-serial file:/tmp/serial.log",
		"-m 1G",
		"-smp 2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestBuildArgsFallsBackToBIOS(t *testing.T) {
	bios := writeFakeFile(t, "OVMF_CODE-fake.fd")
	info := archInfo{
		machine:          "q35",
		pflashCandidates: []firmware{{code: "/nonexistent/edk2.fd"}},
		biosCandidates:   []string{bios},
	}
	args, err := buildArgs(info, Options{DiskPath: "/tmp/d.qcow2", SerialLogPath: "/tmp/s.log"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-bios "+bios) {
		t.Errorf("expected -bios fallback, got %q", joined)
	}
}

func TestBuildArgsCopiesVarsTemplate(t *testing.T) {
	code := writeFakeFile(t, "OVMF_CODE_4M.fd")
	vars := writeFakeFile(t, "OVMF_VARS_4M.fd")
	work := t.TempDir()
	info := archInfo{
		machine:          "q35",
		pflashCandidates: []firmware{{code: code, vars: vars}},
	}
	args, err := buildArgs(info, Options{DiskPath: "/tmp/d.qcow2", SerialLogPath: "/tmp/s.log", WorkDir: work})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	copied := work + "/efivars.fd"
	if !strings.Contains(joined, "if=pflash,format=raw,unit=1,file="+copied) {
		t.Errorf("expected writable vars pflash at %s, got %q", copied, joined)
	}
	if strings.Contains(joined, "file="+vars) {
		t.Errorf("the vars template itself must not be handed to qemu: %q", joined)
	}
	if data, err := os.ReadFile(copied); err != nil || string(data) != "fake firmware" {
		t.Errorf("vars copy = %q, %v", data, err)
	}
}

func TestBuildArgsAttachesVolumes(t *testing.T) {
	pflash := writeFakeFile(t, "edk2-fake.fd")
	info := archInfo{
		machine:          "virt",
		pflashCandidates: []firmware{{code: pflash}},
	}
	args, err := buildArgs(info, Options{
		DiskPath:      "/tmp/bundle/disk.qcow2",
		DiskFormat:    "qcow2",
		SerialLogPath: "/tmp/serial.log",
		Volumes: []VolumeAttachment{
			{Name: "data", Path: "/state/data.qcow2"},
			{Name: "logs", Path: "/state/logs.qcow2"},
		},
	})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"file=/tmp/bundle/disk.qcow2,if=virtio,format=qcow2,snapshot=on",
		"-drive file=/state/data.qcow2,if=none,id=vol0,format=qcow2 -device virtio-blk-pci,drive=vol0,serial=data",
		"-drive file=/state/logs.qcow2,if=none,id=vol1,format=qcow2 -device virtio-blk-pci,drive=vol1,serial=logs",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "snapshot=on,serial=") || strings.Contains(joined, "data.qcow2,if=virtio,format=qcow2,snapshot=on") {
		t.Errorf("a volume drive must not carry snapshot=on: %q", joined)
	}
}

func secureBootInfo(t *testing.T, smm bool) (info archInfo, code, vars string) {
	t.Helper()
	code = writeFakeFile(t, "OVMF_CODE.secboot.fd")
	vars = writeFakeFile(t, "OVMF_VARS.ms.fd")
	plain := writeFakeFile(t, "OVMF_CODE.fd")
	machine := "virt"
	if smm {
		machine = "q35"
	}
	return archInfo{
		machine:              machine,
		pflashCandidates:     []firmware{{code: plain, vars: vars}},
		secureBootCandidates: []firmware{{code: "/nonexistent/code.fd", vars: vars}, {code: code, vars: "/nonexistent/vars.fd"}, {code: code, vars: vars}},
		secureBootSMM:        smm,
	}, code, vars
}

func TestBuildArgsSecureBootAMD64(t *testing.T) {
	info, code, vars := secureBootInfo(t, true)
	work := t.TempDir()
	args, err := buildArgs(info, Options{Arch: "amd64", SecureBoot: true, DiskPath: "/tmp/d.qcow2", SerialLogPath: "/tmp/s.log", WorkDir: work})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-M q35,smm=on",
		"-global driver=cfi.pflash01,property=secure,value=on",
		"if=pflash,format=raw,unit=0,readonly=on,file=" + code,
		"if=pflash,format=raw,unit=1,file=" + work + "/efivars.fd",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "OVMF_CODE.fd") || strings.Contains(joined, "file="+vars) {
		t.Errorf("args use non-Secure-Boot firmware or the vars template itself: %q", joined)
	}
	if data, err := os.ReadFile(work + "/efivars.fd"); err != nil || string(data) != "fake firmware" {
		t.Errorf("vars copy = %q, %v", data, err)
	}
}

func TestBuildArgsSecureBootARM64(t *testing.T) {
	info, code, _ := secureBootInfo(t, false)
	work := t.TempDir()
	args, err := buildArgs(info, Options{Arch: "arm64", SecureBoot: true, DiskPath: "/tmp/d.qcow2", SerialLogPath: "/tmp/s.log", WorkDir: work})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-M virt ", "if=pflash,format=raw,unit=0,readonly=on,file=" + code, "unit=1,file=" + work + "/efivars.fd"} {
		if !strings.Contains(joined+" ", want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	for _, bad := range []string{"smm", "cfi.pflash01"} {
		if strings.Contains(joined, bad) {
			t.Errorf("arm64 args must not contain %q: %q", bad, joined)
		}
	}
}

func TestBuildArgsWithoutSecureBootUnchanged(t *testing.T) {
	info, _, _ := secureBootInfo(t, true)
	args, err := buildArgs(info, Options{Arch: "amd64", DiskPath: "/tmp/d.qcow2", SerialLogPath: "/tmp/s.log", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-M q35 ") || strings.Contains(joined, "smm") || strings.Contains(joined, "cfi.pflash01") || strings.Contains(joined, "secboot") {
		t.Errorf("args changed without Secure Boot: %q", joined)
	}
}

func TestBuildArgsSecureBootErrors(t *testing.T) {
	info, _, _ := secureBootInfo(t, true)
	if _, err := buildArgs(info, Options{Arch: "amd64", SecureBoot: true, DiskPath: "/d", SerialLogPath: "/s"}); err == nil || !strings.Contains(err.Error(), "work directory") {
		t.Errorf("without WorkDir: error = %v", err)
	}

	// Never falls back to the plain firmware, which exists here.
	missing := archInfo{
		machine:              "q35",
		pflashCandidates:     info.pflashCandidates,
		secureBootCandidates: []firmware{{code: "/nonexistent/CODE.secboot.fd", vars: "/nonexistent/VARS.ms.fd"}},
	}
	_, err := buildArgs(missing, Options{Arch: "amd64", SecureBoot: true, DiskPath: "/d", SerialLogPath: "/s", WorkDir: t.TempDir()})
	if err == nil {
		t.Fatal("buildArgs fell back to firmware without Secure Boot")
	}
	for _, want := range []string{"/nonexistent/CODE.secboot.fd", "/nonexistent/VARS.ms.fd", "Microsoft", "ovmf"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	none := archInfo{machine: "virt", pflashCandidates: info.pflashCandidates}
	if _, err := buildArgs(none, Options{Arch: "arm64", SecureBoot: true, DiskPath: "/d", SerialLogPath: "/s", WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "Microsoft") {
		t.Errorf("no candidates: error = %v", err)
	}
}

func TestSecureBootCandidatesAreFixed(t *testing.T) {
	for arch, info := range archTable {
		for _, fw := range info.secureBootCandidates {
			if fw.vars == "" || !strings.Contains(fw.code, "secboot") {
				t.Errorf("%s: secure boot candidate %+v needs a vars file and a secboot code image", arch, fw)
			}
		}
	}
	if !archTable["amd64"].secureBootSMM || archTable["arm64"].secureBootSMM {
		t.Error("only amd64 Secure Boot firmware needs SMM")
	}
}

func TestBuildArgsNoFirmwareFound(t *testing.T) {
	info := archInfo{machine: "virt"}
	if _, err := buildArgs(info, Options{}); err == nil {
		t.Fatalf("expected an error when no firmware candidate exists")
	}
}

func writeFakeFile(t *testing.T, name string) string {
	t.Helper()
	p := t.TempDir() + "/" + name
	if err := os.WriteFile(p, []byte("fake firmware"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWaitForExpectStopsWhenQemuExits(t *testing.T) {
	log := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(log, []byte("booting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	close(exited)
	start := time.Now()
	err := waitForExpect(log, "never-printed", time.Minute, exited, func() error { return errors.New("exit status 1") })
	if !errors.Is(err, errExitedEarly) {
		t.Fatalf("waitForExpect = %v, want errExitedEarly", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waitForExpect took %s, should return as soon as QEMU exits", time.Since(start))
	}
}

func TestWaitForExpectMatchBeatsExit(t *testing.T) {
	log := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(log, []byte("boot-ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	close(exited)
	if err := waitForExpect(log, "boot-ok", time.Minute, exited, func() error { return nil }); err != nil {
		t.Fatalf("waitForExpect = %v, want a match", err)
	}
}

func TestBuildArgsEscapesCommas(t *testing.T) {
	pflash := writeFakeFile(t, "edk2-fake.fd")
	info := archInfo{machine: "virt", pflashCandidates: []firmware{{code: pflash}}}
	args, err := buildArgs(info, Options{
		DiskPath:      "/tmp/app,snapshot=off-latest.aarch64/disk.qcow2",
		DiskFormat:    "qcow2",
		SerialLogPath: "/tmp/serial.log",
		Volumes:       []VolumeAttachment{{Name: "data", Path: "/state/a,b/data.qcow2"}},
	})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"file=/tmp/app,,snapshot=off-latest.aarch64/disk.qcow2,if=virtio,format=qcow2,snapshot=on",
		"file=/state/a,,b/data.qcow2,if=none,id=vol0,format=qcow2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestBuildArgsRejectsUnknownDiskFormat(t *testing.T) {
	pflash := writeFakeFile(t, "edk2-fake.fd")
	info := archInfo{machine: "virt", pflashCandidates: []firmware{{code: pflash}}}
	if _, err := buildArgs(info, Options{DiskPath: "/tmp/d", DiskFormat: "qcow2,file=/etc/x", SerialLogPath: "/tmp/s"}); err == nil {
		t.Fatal("buildArgs accepted an unknown disk format")
	}
}

func TestAccelInfoForeignArchUsesTCG(t *testing.T) {
	foreign := "arm64"
	if runtime.GOARCH == "arm64" {
		foreign = "amd64"
	}
	if accel, cpu := accelInfo(foreign); accel != "tcg" || cpu != "max" {
		t.Errorf("accelInfo(%s) on %s = %s, %s; want tcg, max", foreign, runtime.GOARCH, accel, cpu)
	}
}
