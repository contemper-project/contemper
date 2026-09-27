package qemu

import (
	"errors"
	"os"
	"path/filepath"
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
	exited := make(chan error, 1)
	exited <- errors.New("exit status 1")
	start := time.Now()
	err := waitForExpect(log, "never-printed", time.Minute, exited)
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
	exited := make(chan error, 1)
	exited <- nil
	if err := waitForExpect(log, "boot-ok", time.Minute, exited); err != nil {
		t.Fatalf("waitForExpect = %v, want a match", err)
	}
}
