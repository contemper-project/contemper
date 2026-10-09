# contemper

[![CI](https://github.com/contemper-project/contemper/actions/workflows/ci.yml/badge.svg)](https://github.com/contemper-project/contemper/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/contemper-project/contemper.svg)](https://pkg.go.dev/github.com/contemper-project/contemper)
[![Latest release](https://img.shields.io/github/v/release/contemper-project/contemper)](https://github.com/contemper-project/contemper/releases)
[![License](https://img.shields.io/github/license/contemper-project/contemper)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/contemper-project/contemper)](go.mod)
[![Coverage](https://codecov.io/gh/contemper-project/contemper/graph/badge.svg?token=KVKFFGZCSP)](https://codecov.io/gh/contemper-project/contemper)
[![Docs](https://img.shields.io/badge/docs-contemper--project.github.io-blue)](https://contemper-project.github.io/contemper/)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/contemper-project/contemper/badge)](https://scorecard.dev/viewer/?uri=github.com/contemper-project/contemper)

Build bootable VM images from container images.

contemper takes an OCI image you already built, merges in what a given
virtualization target needs, and writes out a bootable disk. Authoring a
VM image becomes an ordinary container build.

```console
$ <tool> build -t registry.example.com/my-appliance:dev .
$ <tool> push registry.example.com/my-appliance:dev
$ contemper convert --target qemu registry.example.com/my-appliance:dev
$ contemper deploy --to local-qemu my-appliance-dev.aarch64/
```

`<tool>` is `podman` or `docker`; the two commands above are identical
either way. `contemper build` runs the build (with Docker buildx or
podman) and the convert step together and skips the registry; see
[Getting started](docs/getting-started.md) for that walkthrough.

## Why

Containers arguably became what they are because the `Dockerfile` made
image authoring easy and understandable. VM images are still mostly
built with heavier, more bespoke tooling: a separate toolchain with its
own base image conventions, build tools and registry habits, even though
the content (a filesystem plus a little metadata about how to boot it) is
the same kind of thing.

contemper bets that container authoring works just as well for VM
images. You write a build file with the tools you already use, push and
scan it like any other image, and contemper turns it into a disk.
Building a custom VM image should feel no different from building a
custom container image.

## How it works

- **You author, contemper packages.** Your image provides a kernel, an
  initrd and an init system, and is marked with
  `LABEL io.contemper.ready="true"`. contemper checks the label before
  pulling any layers.
- **Two boot modes.** By default (UKI) the image installs the kernel, a
  generic initrd and optionally a kernel command line at
  [fixed paths](docs/guide/authoring.md#the-fixed-path-contract), and
  contemper seals them into a Unified Kernel Image it builds. The image
  is the unit of change: you upgrade by redeploying a new image, with
  state on volumes. Alternatively, an image can ship its own EFI
  bootloader under `/boot/efi` (GRUB, systemd-boot, shim for Secure
  Boot), declared with `LABEL io.contemper.boot="bootloader"`. The guest
  then updates its own kernel with its package manager, so it can run as
  a long-lived VM. contemper does not generate or install the
  bootloader. See
  [choosing a boot mode](docs/guide/bootloader-images.md#choosing-a-boot-mode).
- **Nothing from your image runs at conversion time.** contemper merges
  layers and reads files; it never executes image content. No container
  runtime, no privileged builder, no emulation: an arm64 disk builds on
  an x86-64 host as easily as a native one.
- **The output is a bundle**: a UEFI-bootable qcow2 disk (the
  EFI partition holds either contemper's Unified Kernel Image or the
  image's `/boot/efi` tree; the root is a writable ext4 filesystem) plus
  a `contemper.json` recording where it came from and the boot mode.
- **Targets add what a platform needs** through support images: plain
  OCI images layered on top, never executed.

## How it compares

| Tool | Authoring input | Runs code to build | Scope |
| --- | --- | --- | --- |
| **contemper** | any OCI image | no | image → bootable disk |
| [bootc](https://github.com/bootc-dev/bootc) | `Containerfile` on a bootc base | yes (ostree) | image → disk and in-place updates |
| [d2vm](https://github.com/linka-cloud/d2vm) | `Dockerfile` or a Docker image | yes (`docker build`, chroot) | image → disk, several formats |
| [Packer](https://www.packer.io/) | provisioner scripts | yes (boots an instance) | broad; many clouds |
| [mkosi](https://github.com/systemd/mkosi) | declarative config and packages | yes (distro installers) | OS images, systemd-centric |
| [distrobuilder](https://github.com/lxc/distrobuilder) | YAML definitions | yes | Incus/LXC images |

**bootc** is the closest in idea: it shares the belief that container
authoring suits OS images. **d2vm** is the closest in workflow. The
differences from bootc: bootc starts from its own prescriptive
base images, while contemper accepts any base you install a kernel and
init system into (Alpine and OpenRC included). bootc gives
host-style semantics (`/usr` read-only, `/etc` and `/var` merged
forward) and transactional in-place updates of the image. contemper's
default (UKI) behaves like a container: a writable root kept across
reboots and replaced on redeploy, with persistent data on volumes.
Bootloader images can instead run as long-lived VMs that the distro's
package manager updates in place. What contemper lacks is an
image-based update mechanism of its own, nothing like `bootc switch`:
it stops at producing a disk.

**d2vm** also turns a Dockerfile or a Docker image into a VM disk, but
as a conversion tool: for a supported distribution it installs a
kernel, an init system and a bootloader into the image through
`docker build`, then assembles the disk with loop devices, mounts and a
chroot, which needs root. contemper takes all of that from the image,
runs nothing from it, and needs no root. d2vm covers what contemper
doesn't: BIOS boot, a LUKS-encrypted root, build-time settings such as
a root password, more disk formats and KubeVirt containerDisks.

Packer and virt-builder, in their common cloud-image workflow, customize
an existing image by running code (Packer can also install a full OS
from an ISO); mkosi and distrobuilder build from packages through their
own config formats. contemper's input is whatever your container
tooling already produces. More in
[docs/comparison.md](docs/comparison.md).

## Status

contemper is pre-1.0: the CLI and the bundle format may still change.

**Works today:**

- `convert` for the qemu and incus targets (the latter adds the Incus
  agent through the default
  [`incus-support`](https://github.com/contemper-project/support-images)
  image), `build` (with Docker or podman), and `deploy --to local-qemu`.
- Two boot modes: a UKI built by contemper, or the image's own
  bootloader with in-place kernel updates and Secure Boot support.
- `convert` and `deploy` handle several architectures at once.
- Support images can declare variants, selected by what's actually
  present in your image (an init system, a first-boot mechanism).
- Volumes: declare one with `VOLUME` in your build file, and contemper
  sizes, formats and mounts it in the guest through a first-boot helper,
  persisted across redeploys for `local-qemu` instances.

**Planned:** building with podman, and `publish` and `deploy` for Incus. See the
[roadmap](docs/design/roadmap.md) for the rest.

## Documentation

The documentation lives in [`docs/`](docs/index.md) and is published at
<https://contemper-project.github.io/contemper/>. Start with
[Getting started](docs/getting-started.md), or read the
[design](docs/design/index.md) for the reasoning behind the choices.

## Building and testing

```console
$ go build -o contemper ./cmd/contemper
$ make test   # unit tests
$ make e2e    # build, convert and boot the example image
```

`convert` needs e2fsprogs and `qemu-img`; `deploy` needs `qemu-system-*`
and UEFI firmware. See [host tools](docs/reference/host-tools.md).

## License

Apache License 2.0, see [LICENSE](LICENSE). The embedded systemd-stub
binaries are LGPL-2.1-or-later; see [NOTICE](NOTICE) and
[internal/uki/stubs/README.md](internal/uki/stubs/README.md) for where
they come from and how to verify them.
