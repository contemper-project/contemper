package guestmeta_test

import (
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/guestmeta"
)

func TestRenderBuildMinimal(t *testing.T) {
	out := string(guestmeta.RenderBuild(guestmeta.BuildInfo{
		ContemperVersion: "v0.1.0",
		Target:           "qemu-qcow2",
		Arch:             "arm64",
		Source:           guestmeta.ImageRef{Ref: "ghcr.io/example/app:v1", Digest: "sha256:abc"},
	}))
	for _, want := range []string{
		"contemper.version=v0.1.0\n",
		"target=qemu-qcow2\n",
		"arch=arm64\n",
		"source.ref=ghcr.io/example/app:v1\n",
		"source.digest=sha256:abc\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "support") || strings.Contains(out, "volume-helper") {
		t.Errorf("no support/helper fields should appear when unset:\n%s", out)
	}
}

func TestRenderBuildSupportAndVolumeHelper(t *testing.T) {
	out := string(guestmeta.RenderBuild(guestmeta.BuildInfo{
		ContemperVersion: "v0.1.0",
		Target:           "qemu-qcow2",
		Arch:             "amd64",
		Source:           guestmeta.ImageRef{Ref: "app:v1", Digest: "sha256:aaa"},
		Support:          &guestmeta.ImageRef{Ref: "ghcr.io/example/support:v1", Digest: "sha256:bbb"},
		SupportVariants: []guestmeta.VariantRef{
			{Branch: "init-system", Variant: "openrc", Ref: "ghcr.io/example/support-openrc:v1", Digest: "sha256:ccc"},
			{Branch: "extras", Variant: "none"}, // no-op: must not appear
		},
		VolumeHelper: &guestmeta.ImageRef{Ref: "ghcr.io/contemper-project/volumes-support:v1", Digest: "sha256:ddd"},
		VolumeHelperVariants: []guestmeta.VariantRef{
			{Branch: "init-system", Variant: "systemd", Ref: "ghcr.io/contemper-project/volumes-support-init-system-systemd:v1", Digest: "sha256:eee"},
		},
	}))
	for _, want := range []string{
		"support.ref=ghcr.io/example/support:v1\n",
		"support.digest=sha256:bbb\n",
		"support.variant.init-system=ghcr.io/example/support-openrc:v1@sha256:ccc\n",
		"volume-helper.ref=ghcr.io/contemper-project/volumes-support:v1\n",
		"volume-helper.digest=sha256:ddd\n",
		"volume-helper.variant.init-system=ghcr.io/contemper-project/volumes-support-init-system-systemd:v1@sha256:eee\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "extras") {
		t.Errorf("a no-op variant must not be recorded:\n%s", out)
	}
}

func TestRenderBuildVariantAlreadyDigestQualified(t *testing.T) {
	out := string(guestmeta.RenderBuild(guestmeta.BuildInfo{
		Source: guestmeta.ImageRef{Ref: "app:v1", Digest: "sha256:aaa"},
		VolumeHelper: &guestmeta.ImageRef{
			Ref: "ghcr.io/contemper-project/volumes-support:v1", Digest: "sha256:bbb",
		},
		VolumeHelperVariants: []guestmeta.VariantRef{
			{
				Branch:  "init-system",
				Variant: "openrc",
				Ref:     "ghcr.io/contemper-project/volumes-support-init-system-openrc@sha256:ccc",
				Digest:  "sha256:ccc",
			},
		},
	}))
	want := "volume-helper.variant.init-system=ghcr.io/contemper-project/volumes-support-init-system-openrc@sha256:ccc\n"
	if !strings.Contains(out, want) {
		t.Errorf("output missing %q (digest must not be duplicated); got:\n%s", want, out)
	}
	if strings.Contains(out, "sha256:ccc@sha256:ccc") {
		t.Errorf("digest was duplicated:\n%s", out)
	}
}

func TestRedactLocalRef(t *testing.T) {
	got := guestmeta.RedactLocalRef("oci-archive", "/home/build/secret-project/app.tar")
	if got != "oci-archive:app.tar" {
		t.Errorf("RedactLocalRef = %q, want %q", got, "oci-archive:app.tar")
	}
}

func TestRenderVolumes(t *testing.T) {
	out := string(guestmeta.RenderVolumes([]guestmeta.VolumeLine{
		{Name: "data", SerialPattern: "data", FS: "ext4", Mountpoint: "/data"},
		{Name: "logs", SerialPattern: "logs", FS: "ext4", Mountpoint: "/var/log app"},
	}))
	want := "data data ext4 /data\nlogs logs ext4 /var/log app\n"
	if out != want {
		t.Errorf("RenderVolumes = %q, want %q", out, want)
	}
}

func TestRenderNoSeed(t *testing.T) {
	out := string(guestmeta.RenderNoSeed([]string{"data", "logs"}))
	want := "data\nlogs\n"
	if out != want {
		t.Errorf("RenderNoSeed = %q, want %q", out, want)
	}
}

func TestRenderNoSeedEmpty(t *testing.T) {
	if out := guestmeta.RenderNoSeed(nil); len(out) != 0 {
		t.Errorf("RenderNoSeed(nil) = %q, want empty", out)
	}
}

func TestFstabLine(t *testing.T) {
	got := guestmeta.FstabLine("data", "/data")
	want := "LABEL=data /data ext4 defaults,nofail 0 2"
	if got != want {
		t.Errorf("FstabLine = %q, want %q", got, want)
	}
}

func TestAppendFstabCreatesFile(t *testing.T) {
	updated, changed := guestmeta.AppendFstab(nil, []string{guestmeta.FstabLine("data", "/data")})
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if string(updated) != "LABEL=data /data ext4 defaults,nofail 0 2\n" {
		t.Errorf("updated = %q", updated)
	}
}

func TestAppendFstabPreservesExistingAndAppends(t *testing.T) {
	existing := []byte("LABEL=contemper-root / ext4 rw,relatime 0 1\n")
	line := guestmeta.FstabLine("data", "/data")
	updated, changed := guestmeta.AppendFstab(existing, []string{line})
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "LABEL=contemper-root / ext4 rw,relatime 0 1\n" + line + "\n"
	if string(updated) != want {
		t.Errorf("updated = %q, want %q", updated, want)
	}
}

func TestAppendFstabNoDuplicate(t *testing.T) {
	line := guestmeta.FstabLine("data", "/data")
	existing := []byte(line + "\n")
	updated, changed := guestmeta.AppendFstab(existing, []string{line})
	if changed {
		t.Errorf("expected changed=false for an already-present line")
	}
	if string(updated) != string(existing) {
		t.Errorf("updated = %q, want unchanged %q", updated, existing)
	}
}

func TestAppendFstabAddsRepeatedLineOnce(t *testing.T) {
	updated, changed := guestmeta.AppendFstab([]byte("a\n"), []string{"b", "b", "a"})
	if !changed || string(updated) != "a\nb\n" {
		t.Errorf("AppendFstab = %q, %v; want %q, true", updated, changed, "a\nb\n")
	}
}

func TestAppendFstabAddsMissingTrailingNewline(t *testing.T) {
	existing := []byte("LABEL=contemper-root / ext4 rw,relatime 0 1") // no trailing \n
	line := guestmeta.FstabLine("data", "/data")
	updated, changed := guestmeta.AppendFstab(existing, []string{line})
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "LABEL=contemper-root / ext4 rw,relatime 0 1\n" + line + "\n"
	if string(updated) != want {
		t.Errorf("updated = %q, want %q", updated, want)
	}
}

func TestFstabLineEscapesMountPoint(t *testing.T) {
	got := guestmeta.FstabLine("my-data", "/srv/my data\\x\tz")
	want := `LABEL=my-data /srv/my\040data\134x\011z ext4 defaults,nofail 0 2`
	if got != want {
		t.Errorf("FstabLine = %q, want %q", got, want)
	}
	if n := len(strings.Fields(got)); n != 6 {
		t.Errorf("FstabLine has %d fields, want 6: %q", n, got)
	}
}

func TestESPFstabLine(t *testing.T) {
	const want = "LABEL=ESP /boot/efi vfat umask=0077 0 2"
	if got := guestmeta.ESPFstabLine("ESP"); got != want {
		t.Errorf("ESPFstabLine = %q, want %q", got, want)
	}
}

func TestHasMountPoint(t *testing.T) {
	for name, tc := range map[string]struct {
		fstab string
		want  bool
	}{
		"empty":           {"", false},
		"plain":           {"UUID=1 /boot/efi vfat defaults 0 2\n", true},
		"tabs":            {"UUID=1\t/boot/efi\tvfat\tdefaults\t0\t2", true},
		"trailing slash":  {"UUID=1 /boot/efi/ vfat defaults 0 2\n", true},
		"commented out":   {"# UUID=1 /boot/efi vfat defaults 0 2\n", false},
		"indented":        {"   UUID=1 /boot/efi vfat defaults 0 2\n", true},
		"escaped slash":   {"UUID=1 /boot\\057efi vfat defaults 0 2\n", true},
		"other mount":     {"UUID=1 /boot vfat defaults 0 2\nUUID=2 /boot/efi2 vfat defaults 0 2\n", false},
		"in other field":  {"/boot/efi /mnt none bind 0 0\n", false},
		"one field only":  {"/boot/efi\n", false},
		"space in mount":  {"UUID=1 /boot/efi\\040x vfat defaults 0 2\n", false},
		"later line":      {"/dev/vda2 / ext4 defaults 0 1\n\n# c\nUUID=1 /boot/efi vfat defaults 0 2\n", true},
		"truncated octal": {"UUID=1 /boot/efi\\04 vfat defaults 0 2\n", false},
		"crlf":            {"UUID=1 /boot/efi\r\n", true},
	} {
		if got := guestmeta.HasMountPoint([]byte(tc.fstab), "/boot/efi"); got != tc.want {
			t.Errorf("%s: HasMountPoint = %v, want %v", name, got, tc.want)
		}
	}
	if !guestmeta.HasMountPoint([]byte("UUID=1 /data\\040dir ext4 defaults 0 2\n"), "/data dir") {
		t.Error("escaped space did not match")
	}
}

// fstab(5) separates fields by spaces and tabs only, so other Unicode
// white space inside a mount point belongs to it.
func TestHasMountPointKeepsOtherWhiteSpaceInTheField(t *testing.T) {
	for _, mp := range []string{"/data\u00a0dir", "/data\u0085dir", "/data\u2003dir"} {
		line := guestmeta.FstabLine("data", mp) + "\n"
		if !guestmeta.HasMountPoint([]byte(line), mp) {
			t.Errorf("HasMountPoint does not find %q in %q", mp, line)
		}
	}
}
