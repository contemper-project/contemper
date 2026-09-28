# Changelog

## 0.1.0 (2026-09-28)

The first release of contemper: build bootable VM images from container
images. You author a VM image as an ordinary container build, push it
like any other image, and contemper turns it into a disk.

This is an early, proof-of-concept release. The path from image to
booting VM works end to end and is tested in CI, but expect rough edges,
missing features and breaking changes before 1.0.

### What it does

- **`contemper convert`** turns a contemper-ready OCI image into a
  bundle: a UEFI-bootable qcow2 disk (a Unified Kernel Image on the EFI
  partition, a writable ext4 root) plus `contemper.json`, which records
  the image digests and support images the disk came from. A failed
  conversion leaves no partial bundle behind, and converting again
  replaces the previous bundle as a whole.
- **Nothing from the image runs during conversion.** contemper merges
  layers and reads files; it needs no container runtime, no root and no
  emulation. An arm64 disk builds on an x86-64 host, and the other way
  round.
- **Sources:** registry references (using the docker/podman
  credentials you already have), OCI archives, OCI layouts and docker
  archives. Multi-platform images resolve to the target architecture.
- **Support images** layer what a platform needs on top of your image.
  Their variants let one support image adapt to what it's merged into,
  for example OpenRC vs. systemd.
- **Volumes:** `VOLUME` declarations become separate data disks, sized
  by an image label or at deploy time (`deploy --volume /data=10GiB`),
  and attached per instance. A helper merged into the image formats a
  blank volume on first boot and reuses an existing one, on OpenRC and
  systemd.
- **`contemper deploy --to local-qemu`** boots a bundle under QEMU, with
  KVM or HVF acceleration where available and software emulation for a
  foreign architecture.
- Runs on Linux and macOS, amd64 and arm64.

### Install

- **macOS (Homebrew):** `brew install contemper-project/tap/contemper`
  (also installs e2fsprogs and QEMU).
- **Debian/Ubuntu:** download the `.deb` below and
  `sudo apt install ./contemper_0.1.0_linux_amd64.deb`.
- **Fedora/RHEL:** download the `.rpm` below and
  `sudo dnf install ./contemper-0.1.0-1.x86_64.rpm`.
- **Anything else:** the `.tar.gz` archives below, plus the host tools:
  e2fsprogs (`mkfs.ext4`, `debugfs`, `e2fsck`) and `qemu-img` to convert,
  QEMU and UEFI firmware to deploy.

The packages install what `convert` needs; for `deploy`, add the
emulator and firmware for the bundles you boot. See
[Install](https://contemper-project.github.io/contemper/getting-started/#install)
and [Host tools](https://contemper-project.github.io/contemper/reference/host-tools/).

Every archive and package is listed in `checksums.txt` and carries a
signed build provenance attestation:

```console
$ gh attestation verify contemper_0.1.0_linux_amd64.tar.gz --repo contemper-project/contemper
```

### Getting started

- Documentation: https://contemper-project.github.io/contemper/
- An image needs a kernel, a generic initrd and an init system at fixed
  paths, and the `io.contemper.ready="true"` label. See
  [Authoring images](https://contemper-project.github.io/contemper/guide/authoring/);
  `examples/` has an Alpine (OpenRC) and a Debian (systemd) image.

### Before you rely on it

This release is for trying contemper out, not for production use. CLI
flags and the bundle format will still change (bundles are
`formatVersion` 1). Known limitations:

- An image where a later layer removes or replaces the target of a
  hardlink is rejected.
- Paths and symlink targets longer than about 1000 bytes are rejected.
- A bundle for a foreign architecture boots under software emulation,
  which is slow.

Please report bugs and ideas in the
[issue tracker](https://github.com/contemper-project/contemper/issues),
and security issues as described in
[SECURITY.md](https://github.com/contemper-project/contemper/blob/main/SECURITY.md).
