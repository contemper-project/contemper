# Changelog

## [0.2.0](https://github.com/contemper-project/contemper/compare/v0.1.1...v0.2.0) (2026-09-29)


### Features

* **cli:** add contemper build to build and convert in one step ([59753af](https://github.com/contemper-project/contemper/commit/59753afffffce477e3e9183df735f0b84cee48f0))
* **release:** ship shell completions in archives, packages and the cask ([1575a8c](https://github.com/contemper-project/contemper/commit/1575a8c3a38daeb29662f4961ed9ae08384dcac8))
* **source:** read images from the local Docker daemon via docker-daemon: ([4a3cf9e](https://github.com/contemper-project/contemper/commit/4a3cf9e9c374497ee385453da2e0941d797e8417))


### Bug Fixes

* **cli:** stop subprocesses and clean up temporary files on interrupt ([f3c847d](https://github.com/contemper-project/contemper/commit/f3c847dd70b31685483d15978de0087baa47a0ec))
* **progress:** print no failure line for a step stopped by an interrupt ([5f48f16](https://github.com/contemper-project/contemper/commit/5f48f16955274a124a3be64d3b9c8de41f8a1aaa))
* **qemu:** report a deploy stopped by an interrupt as interrupted ([ccef0f4](https://github.com/contemper-project/contemper/commit/ccef0f476e1cff19556384064dc2e515583382ca))
* **release:** use postflight_steps in the Homebrew cask ([db460fe](https://github.com/contemper-project/contemper/commit/db460fe717f6e17b91a939a33c869864d746f805))


### Documentation

* **install:** document shell completion ([4dc9cbb](https://github.com/contemper-project/contemper/commit/4dc9cbb3250b9985377a4727f63ba16d95a3cc1a))
* keep shell prompts and output out of copied console blocks ([ea6b33b](https://github.com/contemper-project/contemper/commit/ea6b33bf42130c62875bbac8bdc17cf904f5f859))
* **reference:** document the docker-daemon source and build command ([f050896](https://github.com/contemper-project/contemper/commit/f050896417310a5862937792ee9f8d9f394503f3))
* **reference:** note docker/buildx as host tools for build ([42da965](https://github.com/contemper-project/contemper/commit/42da9656b65afe6afcbadfc3c4f025e53b193c07))

## [0.1.1](https://github.com/contemper-project/contemper/compare/v0.1.0...v0.1.1) (2026-09-28)

### Bug fixes

- Files in converted images could get wrong modification times: debugfs
  read some timestamps as calendar dates, and a few were far enough off
  that conversion failed. Convert again with 0.1.1 to get correct times.
- Converting an image with hardlinks into a full directory could
  silently drop the link and fail later in e2fsck. contemper now makes
  room first, and fails right away if debugfs can't create a link.

### Examples and docs

- New Arch Linux example (amd64), boot-tested in CI.
- Install instructions give the correct .deb file name.
- CONTRIBUTING describes how work is planned, prioritized and triaged.

### New contributors

- [@DarkressX](https://github.com/DarkressX) made their first
  contribution in [#89](https://github.com/contemper-project/contemper/pull/89):
  the Arch Linux example, and finding and fixing the modification-time
  and hardlink bugs above.

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
  `sudo apt install ./contemper_0.1.0_amd64.deb`.
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
