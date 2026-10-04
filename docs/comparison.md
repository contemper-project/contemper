# Comparison

| Tool | Authoring input | Runs code to build | Scope |
| --- | --- | --- | --- |
| **contemper** | any OCI image | no | image → bootable disk |
| [bootc](https://github.com/bootc-dev/bootc) | `Containerfile` on a bootc base | yes (ostree) | image → disk **and in-place updates** |
| [d2vm](https://github.com/linka-cloud/d2vm) | `Dockerfile` or a Docker image | yes (`docker build`, chroot) | image → disk, several formats |
| [Packer](https://www.packer.io/) | provisioner scripts | yes (boots an instance) | broad; many clouds, Windows too |
| [mkosi](https://github.com/systemd/mkosi) | declarative config and packages | yes (distro installers) | OS images, systemd-centric |
| [distrobuilder](https://github.com/lxc/distrobuilder) | YAML definitions | yes | Incus/LXC images |
| [virt-builder](https://libguestfs.org/virt-builder.1.html) | existing cloud image | yes | customizing existing images |
| [KubeVirt containerDisk](https://kubevirt.io/) | pre-built disk image | no | transport only, not authoring |

## bootc

The closest comparison is **bootc**, which validates the same thesis,
that container-authoring ergonomics suit OS images, with real vendor
backing. It differs in three ways that matter:

- **Base images.** bootc's base images are prescriptive: you start from
  one built for bootc. contemper accepts any base you install a kernel
  and init system into, Alpine and OpenRC included.
- **Scope.** Transactional in-place updates with `bootc switch` are
  bootc's actual point. contemper has no image-based update mechanism of
  its own: it stops at producing a disk. A
  [bootloader image](guide/bootloader-images.md) can still be updated in
  place by the distro's package manager, but that is the guest's
  business, not contemper's.
- **Semantics.** bootc's ostree-based conversion gives
  atomically-upgraded-host semantics: `/usr` read-only, `/etc` and `/var`
  merged forward. contemper's default (UKI) gives container-like ones: a
  writable root that is kept across reboots and replaced on redeploy,
  with persistent data on volumes. Bootloader images can instead run as
  long-lived VMs, as in
  [choosing a boot mode](guide/bootloader-images.md#choosing-a-boot-mode).

## d2vm

**d2vm** is the closest in workflow: a Dockerfile or Docker image goes
in, a VM disk comes out, from a Go CLI. The differences:

- **Conversion model.** d2vm detects the distribution from
  `/etc/os-release` and supports Ubuntu, Debian, Alpine, CentOS, Rocky
  Linux and AlmaLinux; distroless images are not supported. Unless
  `--raw` is given, it installs a kernel, an init system and a
  bootloader into the image with per-distribution templates run through
  `docker build`. contemper is an authoring tool instead: the image
  brings its own kernel, initrd and init system, for any distro, and
  there is no template list.
- **What runs at conversion.** d2vm builds the disk with loop devices,
  partitioning tools and mounts, so it needs root, or runs itself in a
  privileged Docker container. It installs GRUB by chrooting into the
  image and running the image's `grub-install`. contemper runs nothing
  from the image, and `convert` needs no root and no container runtime.
- **Boot and configuration.** d2vm supports BIOS (syslinux, GRUB) and
  UEFI (GRUB) and can set a root password, hostname, DNS, `/etc/hosts`
  entries, network manager and kernel command line at build time, and
  encrypt the root with LUKS. contemper is UEFI only, either a UKI it
  builds or the image's own bootloader, and keeps machine configuration
  in the image.
- **Output.** d2vm writes qcow2, raw, qed, vdi, vhd or vmdk, and can
  push a KubeVirt containerDisk image. contemper produces a bundle
  (qcow2 plus `contemper.json`), adds targets through support images,
  and has `deploy`.

## The others

- **Packer and virt-builder**, in their common cloud-image workflow,
  start from an existing image that already has a kernel installed, and
  customize it by running code, either by booting an instance or through
  libguestfs. (Packer can also drive a full install from an ISO, which
  installs the kernel itself.) contemper builds the OS from your
  container build instead and runs nothing at conversion time.
- **mkosi and distrobuilder** also build an OS from packages, as contemper
  does, but through their own configuration formats and by running
  distro installers. contemper's input is whatever your container tooling
  already produces.
- **KubeVirt containerDisk** wraps a finished disk image in an OCI image
  as an opaque payload. It solves transport, not authoring.
