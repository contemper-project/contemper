# Changelog

## [0.3.0](https://github.com/contemper-project/contemper/compare/v0.2.0...v0.3.0) (2026-10-04)


### Features

* **bundle:** add the group file format for multi-arch runs ([ce95b4b](https://github.com/contemper-project/contemper/commit/ce95b4b1119b46c63536b1e047d60c7acc583932))
* **bundle:** record the boot mode in the manifest ([0849c18](https://github.com/contemper-project/contemper/commit/0849c1858004c5114a5e08acb48f810fda036141))
* **convert:** boot an image from its own bootloader when it asks to ([0154cfb](https://github.com/contemper-project/contemper/commit/0154cfba42616e792137c976677bcd050280bb64))
* **convert:** convert every requested architecture in one run ([d283159](https://github.com/contemper-project/contemper/commit/d283159d40819036c862dbb1ab3d997c53aa13fc))
* **convert:** support the io.contemper.secure-boot label ([1926e00](https://github.com/contemper-project/contemper/commit/1926e007ece843fca7e951c171ff4cef2ddc6571))
* **convert:** write a group file when several architectures are requested ([13b0f5e](https://github.com/contemper-project/contemper/commit/13b0f5e71f83fa35445ff2a614fc2a3c016b3f34))
* **deploy:** boot the host's architecture from a multi-arch group file ([f584c72](https://github.com/contemper-project/contemper/commit/f584c72caffeb3cfc880b84da18a64d802b5583c))
* **disk:** size the ESP per image and copy a file tree onto it ([39a91b0](https://github.com/contemper-project/contemper/commit/39a91b0f3d3dc446b6fce01fd1eaa73f616ad480))
* **examples:** add a Debian image that boots with its own GRUB ([2608e60](https://github.com/contemper-project/contemper/commit/2608e60c996c6945bb6fdb237fd07e39cc5d59ba))
* **guestmeta:** mount the ESP at /boot/efi in bootloader images ([8fcb1b9](https://github.com/contemper-project/contemper/commit/8fcb1b90c0de4c625f0ae7669e5599d2fded3161))
* **qemu:** boot Secure Boot firmware for bundles that ask for it ([96d7fac](https://github.com/contemper-project/contemper/commit/96d7facb4ffd1406721e5566da2fcd74d84962c8))
* **source:** list a source's platforms and parse multi-arch --arch values ([a1b674b](https://github.com/contemper-project/contemper/commit/a1b674b52b78985f5a0ec04922ca6243962afeed))
* **source:** read the io.contemper.boot label ([fc1cb62](https://github.com/contemper-project/contemper/commit/fc1cb62874c0214e993adf126405610f0e4f312c))
* **validate:** check the bootloader tree of images that bring their own ([8e77a26](https://github.com/contemper-project/contemper/commit/8e77a267c73392f660e67edd6160dd5792d48c6e))
* **volume:** add a per-volume seed opt-out and report it at convert time ([ebe1d9d](https://github.com/contemper-project/contemper/commit/ebe1d9d5f43076c3433c3cc2a5a6d3ed426d58c8))
* **volumes-support:** seed a newly formatted volume from the image's content ([73d1a0e](https://github.com/contemper-project/contemper/commit/73d1a0e4b9544a3efefd905b60bfd0c29cebe2d9))


### Bug Fixes

* **convert:** cap the sizes an image label can request ([465d1f6](https://github.com/contemper-project/contemper/commit/465d1f6742a9412850d1c33fc762b5f2050be8ac))
* **convert:** return a bundle value from the Secure Boot checks ([683a0a5](https://github.com/contemper-project/contemper/commit/683a0a5526b839cb2d1fc83cfdd0b95623e26e27))
* **deploy:** refuse bundle disks that refer to other host files ([3c52a44](https://github.com/contemper-project/contemper/commit/3c52a44718256a65115511ffe15ee81905b1e4d6))
* **deploy:** tie an instance's volumes to the image that created them ([afc8456](https://github.com/contemper-project/contemper/commit/afc845626e81685cae86c76bbac2dd0bf65b2b05))
* **disk:** create the root filesystem with a fixed ext4 feature set ([9bfd1c0](https://github.com/contemper-project/contemper/commit/9bfd1c06fb66d42f2fceb4201c153792160b53fa))
* **disk:** only pass known extended attribute names to debugfs ([bc6bddf](https://github.com/contemper-project/contemper/commit/bc6bddf9530136ff57bc4c100a8c1f0e0f452421))
* **examples:** include fsck in the Debian example's initrd ([c31b398](https://github.com/contemper-project/contemper/commit/c31b3989a7a461dc2131364b18fadb182d046fe2))
* **examples:** include fsck in the Debian GRUB example's initrd ([9baf382](https://github.com/contemper-project/contemper/commit/9baf382177915b788b416923227877533f3b99bf))
* **progress:** escape control characters in printed image strings ([3248fbd](https://github.com/contemper-project/contemper/commit/3248fbdedd2209aa7a3596c581291bb9e40924f0))
* **rootfs:** bound the content read from an image ([82bc823](https://github.com/contemper-project/contemper/commit/82bc8238df7f9ca912ae1c9502632dfd7700a364))
* **support:** keep variant images in the support image's namespace ([d840352](https://github.com/contemper-project/contemper/commit/d8403527be5404b8ba108993c0f4ed5f32ffc130))


### Performance Improvements

* **convert:** keep disk.raw sparse ([14dff18](https://github.com/contemper-project/contemper/commit/14dff186950006049d062aa53e234d0d84fd8b3c))
* **disk:** copy the root filesystem into the disk image sparsely ([039499e](https://github.com/contemper-project/contemper/commit/039499e230844b8e184e3c5db4a9bb0291fb14ad))


### Documentation

* **bootloader:** document images that bring their own bootloader ([74c23a1](https://github.com/contemper-project/contemper/commit/74c23a17b6370c12fd58ebf52c508fd5234c18c2))
* **bootloader:** document Secure Boot for bootloader images ([c016487](https://github.com/contemper-project/contemper/commit/c016487cdee3e80e56ab15cf307d696514fe1719))
* **contributing:** describe adding a distribution to the test matrix ([e945b62](https://github.com/contemper-project/contemper/commit/e945b62f44196dd1f4adabfa4f3d07067e0e4187))
* **contributing:** describe the scheduled distribution tests ([5555cbf](https://github.com/contemper-project/contemper/commit/5555cbf6fb62f344416f99eb9466ebcc37343cd5))
* **contributing:** document bug-reporting and new-functionality testing ([c2680b7](https://github.com/contemper-project/contemper/commit/c2680b787c96a0434395b144833d77dbf577121d))
* **contributing:** point to make lint instead of a bare golangci-lint invocation ([48934d8](https://github.com/contemper-project/contemper/commit/48934d82696946e757934febf9bf3e5d053e8ab7))
* **examples:** note that debian-grub can boot with Secure Boot ([6ae8718](https://github.com/contemper-project/contemper/commit/6ae87180feeef9c8cf053e3b3cd0f89ea72f3a93))
* **getting-started:** lead with contemper build and deploy ([d0bd8f4](https://github.com/contemper-project/contemper/commit/d0bd8f4e14f9f4608d89f53153e0ad176c156fcf))
* **guide:** add a distributions page ([383bb04](https://github.com/contemper-project/contemper/commit/383bb0463c1efb70af0c38744e0ef4b7f4423bd4))
* **guide:** describe long-lived VMs and what changes for them ([f1a1bdf](https://github.com/contemper-project/contemper/commit/f1a1bdffd20442ab7c6f1d244ea02f2c256b7bf3))
* **guide:** describe the fixed root filesystem feature set ([b85f2f3](https://github.com/contemper-project/contemper/commit/b85f2f3c02c2b33225242ddd871d149071e1ab83))
* **guide:** explain choosing between UKI and bootloader images ([7dbd2c3](https://github.com/contemper-project/contemper/commit/7dbd2c343722e8a9f281d44c5b7499da7805ca37))
* **guide:** name the openSUSE arm64 kernel image ([3becd0d](https://github.com/contemper-project/contemper/commit/3becd0d927444199fbe13fd1b42bf72d97f5773b))
* **guide:** note FSTYPE for initramfs-tools root fsck ([e366c01](https://github.com/contemper-project/contemper/commit/e366c012fef43e6c9cd2a842d13dc949105f19fe))
* **guide:** note that disk.raw is written sparse ([b9dde34](https://github.com/contemper-project/contemper/commit/b9dde348c8a7216663f0402de09f38aaa319ee2f))
* **multi-arch:** document converting every architecture and the group file ([72b9815](https://github.com/contemper-project/contemper/commit/72b981559dd79bfacb315d8eb661cbac5f1abc62))
* **multi-arch:** document deploying from a group file ([37228f6](https://github.com/contemper-project/contemper/commit/37228f6a21d97c3bf422845ae1d37f75a73b4f73))
* **security:** add a direct reporting link and a disclosure timeline ([ca1c592](https://github.com/contemper-project/contemper/commit/ca1c592f681e87960a54e93c29790e3101455875))
* **uki:** explain where the embedded systemd-stub binaries come from ([64c53d3](https://github.com/contemper-project/contemper/commit/64c53d38d6a16eea4bdafdb8b1edb6db3b71fdc4))
* **volume:** document seeding a volume from the image's content ([68dfbf1](https://github.com/contemper-project/contemper/commit/68dfbf1ca763fd269ac5468a2d5fa53600cad379))

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
