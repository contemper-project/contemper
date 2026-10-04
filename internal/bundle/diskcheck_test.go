package bundle

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// qcow2Bytes builds a minimal qcow2 header of the given version, with
// optional incompatible features, backing file offset and extensions
// (type, payload pairs), followed by the zero padding of a 64 KiB
// header cluster.
func qcow2Bytes(version uint32, incompat uint64, backingOff uint64, exts ...any) []byte {
	b := make([]byte, 1<<16)
	binary.BigEndian.PutUint32(b[0:], qcow2Magic)
	binary.BigEndian.PutUint32(b[4:], version)
	binary.BigEndian.PutUint64(b[8:], backingOff)
	binary.BigEndian.PutUint32(b[20:], 16)
	binary.BigEndian.PutUint64(b[24:], 1<<20)
	p := 72
	if version == 3 {
		binary.BigEndian.PutUint64(b[72:], incompat)
		binary.BigEndian.PutUint32(b[96:], 4)
		binary.BigEndian.PutUint32(b[100:], 112)
		p = 112
	}
	for i := 0; i+1 < len(exts); i += 2 {
		payload := exts[i+1].([]byte)
		binary.BigEndian.PutUint32(b[p:], exts[i].(uint32))
		binary.BigEndian.PutUint32(b[p+4:], uint32(len(payload)))
		copy(b[p+8:], payload)
		p += 8 + (len(payload)+7)&^7
	}
	return b
}

func TestCheckQcow2Header(t *testing.T) {
	good := qcow2Bytes(3, 0, 0, uint32(0xe2792aca), []byte("raw"))
	short := qcow2Bytes(3, 0, 0)[:100]
	badMagic := qcow2Bytes(3, 0, 0)
	badMagic[0] = 'X'
	cases := []struct {
		name    string
		img     []byte
		wantErr string
	}{
		{"v3 plain", qcow2Bytes(3, 0, 0), ""},
		{"v2 plain", qcow2Bytes(2, 0, 0), ""},
		{"dirty bit", qcow2Bytes(3, 1, 0), ""},
		{"known extension", good, ""},
		{"backing file", qcow2Bytes(3, 0, 0x200), "backing file"},
		{"backing file v2", qcow2Bytes(2, 0, 0x68), "backing file"},
		{"data file bit", qcow2Bytes(3, 1<<2, 0), "external data file"},
		{"data file extension", qcow2Bytes(3, 0, 0, uint32(qcow2ExtDataFile), []byte("/etc/shadow")), "external data file"},
		{"data file extension after another", qcow2Bytes(3, 0, 0, uint32(0xe2792aca), []byte("raw"), uint32(qcow2ExtDataFile), []byte("/x")), "external data file"},
		{"unknown incompatible bit", qcow2Bytes(3, 1<<5, 0), "does not know"},
		{"bad magic", badMagic, "bad magic"},
		{"bad version", qcow2Bytes(4, 0, 0), "version"},
		{"truncated", short, "truncated"},
		{"too short", []byte("QFI"), "too short"},
		{"extension past the cluster", func() []byte {
			b := qcow2Bytes(3, 0, 0)
			binary.BigEndian.PutUint32(b[112:], 0xe2792aca)
			binary.BigEndian.PutUint32(b[116:], 1<<30)
			return b
		}(), "truncated qcow2 header extension"},
	}
	for _, c := range cases {
		err := checkQcow2Header(c.img)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func writeDisk(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDisk(t *testing.T) {
	m := &Manifest{Disk: DiskInfo{File: "disk.qcow2", Format: "qcow2"}}

	t.Run("regular qcow2", func(t *testing.T) {
		dir := t.TempDir()
		writeDisk(t, dir, "disk.qcow2", qcow2Bytes(3, 0, 0))
		got, err := VerifyDisk(dir, m)
		if err != nil || got != filepath.Join(dir, "disk.qcow2") {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("backing file", func(t *testing.T) {
		dir := t.TempDir()
		writeDisk(t, dir, "disk.qcow2", qcow2Bytes(3, 0, 0x200))
		if _, err := VerifyDisk(dir, m); err == nil || !strings.Contains(err.Error(), "backing file") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not a qcow2", func(t *testing.T) {
		dir := t.TempDir()
		writeDisk(t, dir, "disk.qcow2", []byte(strings.Repeat("x", 4096)))
		if _, err := VerifyDisk(dir, m); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "host")
		writeDisk(t, filepath.Dir(target), "host", []byte("secret"))
		if err := os.Symlink(target, filepath.Join(dir, "disk.raw")); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyDisk(dir, &Manifest{Disk: DiskInfo{File: "disk.raw", Format: "raw"}})
		if err == nil || !strings.Contains(err.Error(), "a symlink") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("hard link", func(t *testing.T) {
		dir := t.TempDir()
		host := filepath.Join(t.TempDir(), "host")
		writeDisk(t, filepath.Dir(host), "host", []byte("secret"))
		if err := os.Link(host, filepath.Join(dir, "disk.raw")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		_, err := VerifyDisk(dir, &Manifest{Disk: DiskInfo{File: "disk.raw", Format: "raw"}})
		if err == nil || !strings.Contains(err.Error(), "hard links") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("raw is not parsed", func(t *testing.T) {
		dir := t.TempDir()
		writeDisk(t, dir, "disk.raw", []byte("x"))
		if _, err := VerifyDisk(dir, &Manifest{Disk: DiskInfo{File: "disk.raw", Format: "raw"}}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := VerifyDisk(t.TempDir(), m); err == nil {
			t.Fatal("expected error")
		}
	})
}

// TestVerifyDiskRealQcow2 checks real qemu-img output, when qemu-img is
// available: a plain image passes, one with a backing file or an external
// data file is refused.
func TestVerifyDiskRealQcow2(t *testing.T) {
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img not found")
	}
	dir := t.TempDir()
	host := filepath.Join(t.TempDir(), "host.raw")
	writeDisk(t, filepath.Dir(host), "host.raw", make([]byte, 1<<20))
	m := &Manifest{Disk: DiskInfo{File: "disk.qcow2", Format: "qcow2"}}
	create := func(args ...string) error {
		out, err := exec.CommandContext(t.Context(), qemuImg, append([]string{"create", "-q", "-f", "qcow2"}, args...)...).CombinedOutput()
		if err != nil {
			t.Logf("qemu-img: %s", out)
		}
		return err
	}
	path := filepath.Join(dir, "disk.qcow2")

	if err := create(path, "1M"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDisk(dir, m); err != nil {
		t.Errorf("plain image: %v", err)
	}

	_ = os.Remove(path)
	if err := create("-b", host, "-F", "raw", path, "1M"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDisk(dir, m); err == nil || !strings.Contains(err.Error(), "backing file") {
		t.Errorf("backing file: err = %v", err)
	}

	_ = os.Remove(path)
	if err := create("-o", "data_file="+host+",data_file_raw=on", path, "1M"); err != nil {
		t.Skipf("this qemu-img cannot create an external data file image: %v", err)
	}
	if _, err := VerifyDisk(dir, m); err == nil || !strings.Contains(err.Error(), "external data file") {
		t.Errorf("data file: err = %v", err)
	}
}

func TestReadRejectsUnknownDiskFormat(t *testing.T) {
	dir := t.TempDir()
	data := `{"schemaVersion":1,"arch":"amd64","disk":{"file":"d","format":"vmdk"}}`
	writeDisk(t, dir, ManifestFile, []byte(data))
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "disk.format") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadRefusesIrregularManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/zero", filepath.Join(dir, ManifestFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlink to /dev/zero: err = %v", err)
	}

	dir = t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ManifestFile), 0o600); err != nil {
		t.Skipf("no FIFOs: %v", err)
	}
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("FIFO: err = %v", err)
	}

	dir = t.TempDir()
	writeDisk(t, dir, ManifestFile, []byte(`{"x":"`+strings.Repeat("a", maxManifestSize)+`"}`))
	if _, err := Read(dir); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("oversize: err = %v", err)
	}
}

func TestReadGroupRefusesIrregularFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x"+GroupSuffix)
	if err := os.Symlink("/dev/zero", p); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGroup(p); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("err = %v", err)
	}
}
