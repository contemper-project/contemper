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
