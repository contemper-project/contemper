package disk_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/contemper-project/contemper/internal/disk"
	"github.com/contemper-project/contemper/internal/hostenv"
	"github.com/contemper-project/contemper/internal/imgtest"
	"github.com/contemper-project/contemper/internal/rootfs"
)

func requireExt4Tools(t *testing.T) (debugfs, e2fsck string) {
	t.Helper()
	for _, name := range []string{"mkfs.ext4", "debugfs", "e2fsck"} {
		if hostenv.Find(name) == "" {
			t.Skipf("%s not found; skipping ext4 population test", name)
		}
	}
	return hostenv.Find("debugfs"), hostenv.Find("e2fsck")
}

func debugfsStat(t *testing.T, debugfsPath, img, path string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), debugfsPath, "-R", "stat "+path, img).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs stat %s: %v\n%s", path, err, out)
	}
	return string(out)
}

func TestPopulateExt4(t *testing.T) {
	debugfsPath, e2fsckPath := requireExt4Tools(t)

	files := []imgtest.File{
		{Path: "etc/", Typeflag: tar.TypeDir, Mode: 0o750, UID: 0, GID: 0},
		{Path: "etc/hostname", Data: []byte("test-vm\n"), Mode: 0o644, UID: 0, GID: 0},
		{Path: "etc/owned", Data: []byte("mine\n"), Mode: 0o600, UID: 1000, GID: 1000},
		{Path: "etc/link-to-hostname", Typeflag: tar.TypeSymlink, Linkname: "hostname"},
		{Path: "etc/hardlink-to-hostname", Typeflag: tar.TypeLink, Linkname: "etc/hostname"},
		{Path: "dev/", Typeflag: tar.TypeDir},
		{Path: "dev/null", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3, Mode: 0o666},
		{Path: "a file with spaces.txt", Data: []byte("spacey\n")},
	}
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "amd64"}, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("rootfs.Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	imgPath := t.TempDir() + "/root.img"
	warnings, err := disk.PopulateExt4(rfs, imgPath, disk.Ext4Options{
		Label:     "contemper-root",
		SizeBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("PopulateExt4: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	// e2fsck -fn is the authoritative correctness check.
	out, err := exec.CommandContext(context.Background(), e2fsckPath, "-fn", imgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("e2fsck -fn reported problems: %v\n%s", err, out)
	}

	hostnameStat := debugfsStat(t, debugfsPath, imgPath, "/etc/hostname")
	if !strings.Contains(hostnameStat, "Mode:  0644") {
		t.Errorf("hostname mode: %s", hostnameStat)
	}
	if !strings.Contains(hostnameStat, "Links: 2") {
		t.Errorf("hostname should have 2 links (itself + hardlink): %s", hostnameStat)
	}

	ownedStat := debugfsStat(t, debugfsPath, imgPath, "/etc/owned")
	if !strings.Contains(ownedStat, "User:  1000") && !strings.Contains(ownedStat, "User:1000") && !strings.Contains(ownedStat, "User:     1000") {
		t.Errorf("owned uid not applied: %s", ownedStat)
	}

	linkStat := debugfsStat(t, debugfsPath, imgPath, "/etc/link-to-hostname")
	if !strings.Contains(linkStat, "Type: symlink") || !strings.Contains(linkStat, `Fast link dest: "hostname"`) {
		t.Errorf("symlink not as expected: %s", linkStat)
	}

	devStat := debugfsStat(t, debugfsPath, imgPath, "/dev/null")
	if !strings.Contains(devStat, "Type: character special") || !strings.Contains(devStat, "01:03") {
		t.Errorf("device node not as expected: %s", devStat)
	}

	spaceStat := debugfsStat(t, debugfsPath, imgPath, `"/a file with spaces.txt"`)
	if !strings.Contains(spaceStat, "Type: regular") {
		t.Errorf("file with spaces not as expected: %s", spaceStat)
	}
}

// debugfsRun runs an arbitrary read-only debugfs request against img and
// returns its combined output.
func debugfsRun(t *testing.T, debugfsPath, img, request string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), debugfsPath, "-R", request, img).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs %s: %v\n%s", request, err, out)
	}
	return string(out)
}

// TestPopulateExt4Xattrs checks that extended attributes - in particular
// a binary security.capability value, the mechanism Linux uses for file
// capabilities (e.g. on ping) - survive both the layer merge and ext4
// population, rather than being silently dropped.
func TestPopulateExt4Xattrs(t *testing.T) {
	debugfsPath, e2fsckPath := requireExt4Tools(t)

	// A realistic security.capability value: VFS_CAP_REVISION_2 with the
	// effective flag set, granting CAP_NET_RAW - what
	// "setcap cap_net_raw+ep" on ping produces.
	capValue := []byte{
		0x01, 0x00, 0x00, 0x02, // magic_etc: VFS_CAP_REVISION_2 | VFS_CAP_FLAGS_EFFECTIVE
		0x00, 0x20, 0x00, 0x00, // data[0].permitted: CAP_NET_RAW (bit 13)
		0x00, 0x00, 0x00, 0x00, // data[0].inheritable
		0x00, 0x00, 0x00, 0x00, // data[1].permitted
		0x00, 0x00, 0x00, 0x00, // data[1].inheritable
	}

	files := []imgtest.File{
		{Path: "usr/", Typeflag: tar.TypeDir},
		{
			Path:     "usr/bin/",
			Typeflag: tar.TypeDir,
			Xattrs:   map[string]string{"user.dirattr": "on a directory"},
		},
		{
			Path: "usr/bin/ping",
			Data: []byte("#!/bin/sh\necho pretend-ping\n"),
			Mode: 0o755,
			Xattrs: map[string]string{
				"security.capability": string(capValue),
				"user.test":           "hello xattr",
			},
		},
	}
	img, err := imgtest.Image(v1.Platform{OS: "linux", Architecture: "amd64"}, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	rfs, err := rootfs.Build(img, nil)
	if err != nil {
		t.Fatalf("rootfs.Build: %v", err)
	}
	defer func() { _ = rfs.Close() }()

	// The merge (layer -> flattened rootfs tar) must keep the xattrs
	// before ext4 population even runs.
	pingEntry, ok := rfs.Lookup("/usr/bin/ping")
	if !ok {
		t.Fatalf("merged rootfs is missing /usr/bin/ping")
	}
	if got := pingEntry.Header.PAXRecords["SCHILY.xattr.security.capability"]; got != string(capValue) {
		t.Fatalf("merge dropped security.capability: got %q", got)
	}

	imgPath := t.TempDir() + "/root.img"
	warnings, err := disk.PopulateExt4(rfs, imgPath, disk.Ext4Options{
		Label:     "contemper-root",
		SizeBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("PopulateExt4: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	// e2fsck -fn is the authoritative correctness check.
	if out, err := exec.CommandContext(context.Background(), e2fsckPath, "-fn", imgPath).CombinedOutput(); err != nil {
		t.Fatalf("e2fsck -fn reported problems: %v\n%s", err, out)
	}

	pingList := debugfsRun(t, debugfsPath, imgPath, "ea_list /usr/bin/ping")
	if !strings.Contains(pingList, "security.capability (20)") {
		t.Errorf("security.capability not set on ping: %s", pingList)
	}
	if !strings.Contains(pingList, `user.test (11) = "hello xattr"`) {
		t.Errorf("user.test not set on ping: %s", pingList)
	}

	dirList := debugfsRun(t, debugfsPath, imgPath, "ea_list /usr/bin")
	if !strings.Contains(dirList, `user.dirattr`) {
		t.Errorf("user.dirattr not set on /usr/bin: %s", dirList)
	}

	// Round-trip the binary value exactly, not just its length.
	capOut := t.TempDir() + "/cap.bin"
	if out, err := exec.CommandContext(context.Background(), debugfsPath, "-R", fmt.Sprintf("ea_get -f %s /usr/bin/ping security.capability", capOut), imgPath).CombinedOutput(); err != nil {
		t.Fatalf("ea_get: %v\n%s", err, out)
	}
	got, err := os.ReadFile(capOut)
	if err != nil {
		t.Fatalf("reading ea_get output: %v", err)
	}
	if !bytes.Equal(got, capValue) {
		t.Errorf("security.capability value mismatch: got % x, want % x", got, capValue)
	}
}
