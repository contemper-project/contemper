# Bootloader images

!!! warning "Planned"
    Nothing on this page is implemented yet. It records the contract that
    the implementation will follow. Today every image boots as a Unified
    Kernel Image (UKI) assembled by `convert`.

## The problem

By default `convert` takes the kernel, initrd and command line from
`/boot/contemper/` and seals them into a UKI on the ESP. That makes the
kernel a build-time input: to get a new kernel you build a new image and
replace the disk, which is the right model for an appliance whose
persistent state lives on [volumes](../guide/volumes.md).

Some images want the opposite: a VM that updates its kernel in place with
its package manager, like a regular installation. That needs a real EFI
bootloader on the ESP (GRUB, systemd-boot, ...) that reads the kernel from
disk at every boot. A **bootloader image** is an image that brings its own.

## Declaration

An image label selects the mode:

```dockerfile
LABEL io.contemper.boot="bootloader"
```

| Value | Meaning |
| --- | --- |
| `uki` | contemper assembles a UKI from `/boot/contemper/` (today's behavior) |
| `bootloader` | the image's own bootloader is copied onto the ESP |

An absent label means `uki`. Any other value is a convert error. Being a
label, it is read from the image config before any layer is fetched, like
`io.contemper.ready`.

## Disk layout

The disk keeps the shape described in [the disk and VM
lifecycle](../guide/disk.md): an ESP, then the root last. Only the ESP
differs.

| # | Partition | `uki` | `bootloader` |
| --- | --- | --- | --- |
| 1 | EFI system partition, FAT32 | 128 MiB, the UKI at the fallback path | 512 MiB, the image's `/boot/efi` tree |
| 2 | root, ext4, label `contemper-root` | the merged filesystem | the merged filesystem, `/boot/efi` included |

The ESP is larger so that in-guest updates have room when the bootloader
keeps kernels on the ESP. The disk is a sparse qcow2, so the space costs
nothing until it is used.

## What goes where

**ESP.** The image's `/boot/efi` directory tree is copied onto the ESP as
is. It must contain the UEFI removable-media fallback file
`EFI/BOOT/BOOTX64.EFI` (amd64) or `EFI/BOOT/BOOTAA64.EFI` (arm64), because
a fresh VM has no NVRAM boot entries and the firmware can only find the
fallback path. Everything else in the tree is copied too: a distribution
path such as `EFI/debian/`, a `grub.cfg` stub next to the binary,
systemd-boot's `loader/` entries, kernels for bootloaders that read them
from the ESP.

**Root filesystem.** The full image root, `/boot/efi` included, goes on
the ext4 root partition as it does today. The guest is to mount the ESP
at `/boot/efi`, so what the image had there is what the guest sees; the
copy on the root filesystem is only what is underneath the mount.
Kernels and GRUB's own configuration normally live on the root
filesystem, which GRUB reads natively.

**Bootloader configuration is the image's job.** It must find the root
partition by its label, `contemper-root`: `root=LABEL=contemper-root` on
the kernel command line, `search --label contemper-root` in GRUB.
`/boot/contemper/` (vmlinuz, initrd, cmdline) is neither used nor
required in this mode. If present, `convert` reports that it is ignored;
that is not an error.

**What can go onto FAT.** Regular files and directories only.

- A symlink, device node, FIFO or socket in the `/boot/efi` tree is a
  convert error naming the path. A symlink is not followed.
- Hardlinks become independent copies.
- Ownership, modes and xattrs are dropped.
- Names must be valid FAT long file names (VFAT); an invalid name is an
  error.
- The tree must fit the ESP; if not, the error gives both sizes.
- `/boot/efi` itself being a symlink is resolved inside the image, like
  the fixed paths are.

## What convert checks

Without executing anything from the image, `convert` validates:

- the `io.contemper.boot` value;
- `/boot/efi` exists and is a directory;
- the fallback file exists, is a regular file, and is a PE32+ image whose
  COFF machine type matches the target architecture (`0x8664` for amd64,
  `0xAA64` for arm64) and whose optional-header subsystem is EFI
  application (10).

Nothing else about the bootloader or its configuration is checked. A wrong
configuration shows up at boot; `deploy --expect` is the way to catch it
before shipping.

## What contemper does not do

- It generates no bootloader configuration and does not edit the image's.
- It does not install a bootloader or register boot entries; the image
  ships its bootloader already installed under `/boot/efi`.
- It does not check that the kernels or configuration the bootloader will
  look for exist.
- Secure Boot (shim) is out of scope here and tracked separately.

## Bundle manifest

The [bundle manifest](../reference/bundle.md) records the mode in an
additive field, `boot`, with the value `"uki"` or `"bootloader"`.

## Interaction with other features

- **Targets.** `deploy --to local-qemu` needs nothing new: the firmware
  boots the fallback path either way.
- **Support images and volumes** work as in UKI mode.
- **Volumes and in-place updates.** An image that updates itself in place
  keeps its root disk, which is at odds with replacing the disk on
  redeploy; the trade-off is for the guide on choosing between the two
  modes to explain. Persistent data still belongs on volumes.

## Open points for the follow-up issues

- **ESP mount in the guest.** The fstab entry for `/boot/efi` needs a way
  to find the ESP, for example a FAT volume label or a GPT partition
  label. Decided with the implementation of the mount.
- **Debian and GRUB example.** The recipe for installing GRUB to the
  removable path in a container build (packages per architecture,
  `grub-install` options, a `grub.cfg` that searches by label) is left to
  the example image, where it is tested by booting it.
- **Choosing between modes.** The user-facing guide comparing UKI and
  bootloader images follows once the feature exists.
