# Bootloader images

!!! info "Implemented"
    `convert` implements this contract: it reads the label, validates
    `/boot/efi`, builds the larger ESP and records the mode in the bundle
    manifest, and the guest mounts the ESP at `/boot/efi`.
    `examples/debian-grub` is a complete Debian image built this way.

For a practical comparison of the two modes and how to build a
bootloader image, see the [guide](../guide/bootloader-images.md).

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

**Bootloader configuration is the image's job.** The configuration the
image ships must find the root partition by its label, `contemper-root`:
`root=LABEL=contemper-root` on the kernel command line,
`search --label contemper-root` in GRUB. The label is the rule for the
shipped configuration because the root filesystem, and with it its UUID,
only exists once `convert` creates it. A configuration regenerated inside
the guest, by `update-grub` say, may use the UUID: it stays the same for
the life of that disk.
`/boot/contemper/` (vmlinuz, initrd, cmdline) is neither used nor
required in this mode. If present, `convert` reports that it is ignored;
that is not an error.

**What can go onto FAT.** Regular files and directories only.

- A symlink, device node, FIFO or socket in the `/boot/efi` tree is a
  convert error naming the path. A symlink is not followed.
- Hardlinks become independent copies.
- Ownership, modes and xattrs are dropped.
- Names must be printable ASCII without the characters FAT forbids
  (`\ / : * ? " < > |`), at most 255 characters, not starting with a dot
  and not ending in a space or dot. Other names (non-ASCII letters,
  dot-leading names such as `.hidden`) are a convert error: the FAT writer
  contemper uses cannot store them.
- Two names in one directory must not map to the same 8.3 short name. The
  FAT writer only adds a unique numeric tail when the short-name stem is
  longer than eight characters; a name that fits 8.3 after folding (case,
  spaces, a lossy extension cut to three characters) is used as it is, so
  `a b` and `ab`, `foo.conf` and `foo.cons`, or names differing only in
  case would get the same short name: one file would overwrite the other,
  or `fsck.fat` would rename an entry at boot and orphan its long name.
  These are rejected, naming both paths. Ordinary trees are not
  affected (GRUB with shim under `EFI/debian/`, systemd-boot `loader/entries/`
  files with a machine id and kernel version in the name). Known effect:
  a systemd-boot layout with a `<machine-id>/<kver>/` directory per kernel
  is rejected when two Debian- or Ubuntu-style kernel versions
  (`6.1.0-25-amd64` and `6.1.0-26-amd64`) sit in one directory. This is a
  limitation of the writer and can be relaxed once it generates unique
  short names for such names.
- The tree must fit the ESP, counting FAT overhead: each file takes
  whole clusters (512 bytes on a 128 MiB ESP, 4 KiB on the 512 MiB one),
  each directory takes at least one cluster, and long names take extra
  directory entries. If it does not fit, the error gives the space needed
  and the ESP's size and usable space.
- An empty file still gets a cluster from the writer, so `fsck.fat` may
  report it; this is harmless.
- `/boot/efi` itself being a symlink is resolved inside the image, like
  the fixed paths are.

## What convert checks

Without executing anything from the image, `convert` validates:

- the `io.contemper.boot` value;
- `/boot/efi` exists and is a directory;
- the fallback file exists (matched case-insensitively, as FAT and the
  firmware do), is a regular file, and is a PE32+ image whose
  COFF machine type matches the target architecture (`0x8664` for amd64,
  `0xAA64` for arm64) and whose optional-header subsystem is EFI
  application (10).

`/sbin/init` is not required in this mode, since the bootloader
configuration may pass `init=`; `convert` prints a warning when it does not
resolve inside the image. Nothing else about the bootloader or its
configuration is checked. A wrong
configuration shows up at boot; `deploy --expect` is the way to catch it
before shipping.

## What contemper does not do

- It generates no bootloader configuration and does not edit the image's.
- It does not install a bootloader or register boot entries; the image
  ships its bootloader already installed under `/boot/efi`.
- It does not check that the kernels or configuration the bootloader will
  look for exist.
- It does not provide Secure Boot support. See [Secure
  Boot](#secure-boot).

## In-place updates

A bootloader image is meant to update itself in the guest: the package
manager installs kernels and reinstalls the bootloader on the VM's own
disk. Two things make that work.

**Distribution tools cannot run in a container build.** Tools such as
`update-grub` and `grub-install` probe the root device, and in a
container build the root is an overlay with no device behind it, so they
fail. The image therefore cannot generate its final configuration or
install its bootloader at build time. Package scripts that would run
them detect the container and skip them.

**Bootstrap, then hand over.** The image ships a minimal bootstrap
configuration, written by hand and finding the root by label, plus the
bootloader binaries copied into place. It only has to boot the first
time. Once the guest runs the distribution's own tool (a first-boot unit
can do it, and a kernel install triggers it through the kernel hook),
the generated configuration
replaces the bootstrap one, with the real filesystem's UUID. From then on
the VM updates like a regular installation. The tool's hook may be gated
on the bootstrap file existing, so ship one even if it is only a
placeholder.

**No boot entries.** A fresh VM has no persistent NVRAM boot entries, so
an in-guest bootloader install must keep the removable-media fallback
path (`EFI/BOOT/`) current, or must not touch NVRAM. For GRUB's
Debian packages that means answering the debconf questions
`grub2/force_efi_extra_removable` with true and `grub2/update_nvram`
with false when building the image. A tool that registers a boot entry
instead would be writing something the VM does not keep. For the same
reason, shim's fallback tool (`fb<arch>.efi`), which writes boot entries
and resets the machine, is not shipped by the example.

**Across deploys.** `deploy --to local-qemu` boots the disk with a
throwaway snapshot overlay: in-guest updates survive reboots of the
guest but not a new deploy, which starts from the bundle as converted.
The [guide](../guide/bootloader-images.md#choosing-a-boot-mode) covers
this.

## Secure Boot

An image may ship shim plus a signed bootloader at the fallback path;
the example does. With Secure Boot disabled, shim simply loads the signed
bootloader from its own directory. Booting with Secure Boot enabled is
tracked separately. Shim
is a PE32+ EFI application, so the check on the fallback file accepts it.

## Bundle manifest

The [bundle manifest](../reference/bundle.md) records the mode in an
additive field, `boot`, with the value `"uki"` or `"bootloader"`.

## Interaction with other features

- **Targets.** `deploy --to local-qemu` needs nothing new: the firmware
  boots the fallback path either way.
- **Support images and volumes** work as in UKI mode.
- **Volumes and in-place updates.** An image that updates itself in place
  keeps its root disk, which is at odds with replacing the disk on
  redeploy; the [guide](../guide/bootloader-images.md#choosing-a-boot-mode)
  explains the trade-off. Persistent data still belongs on volumes.

## Mounting the ESP in the guest

Kernel and bootloader updates inside the guest only reach the ESP if it
is mounted where the distribution expects it. In bootloader mode
`convert` appends one line to the image's `/etc/fstab`:

```text
LABEL=ESP /boot/efi vfat umask=0077 0 2
```

The ESP is found by its FAT volume label, `ESP`, which contemper sets
when it creates the filesystem. The label cannot collide with a volume:
contemper's volume labels are lowercase (`[a-z0-9-]`) and label matching
is case-sensitive. `umask=0077` keeps the FAT files root-only.

There is deliberately no `nofail`, unlike the lines for volumes. The ESP
is on the boot disk itself, so it is always present. With `nofail`,
systemd does not order the mount before `local-fs.target`, and anything
that writes to `/boot/efi` early in boot (a package's postinst script
run by unattended upgrades, say) could race the mount and write into the
root filesystem's copy of the directory underneath, never reaching the
ESP. Without it the mount is part of `local-fs.target`, as Debian's own
default ESP entry is. A corrupt ESP then stops boot in emergency mode, as
on a regular installation. The image's own `/boot/efi` content stays on
the root filesystem underneath the mount, unused once the ESP is
mounted.

- **Existing entry wins.** If the image's fstab already has an entry
  whose mount point is `/boot/efi`, or the directory `/boot/efi`
  resolves to when it is a symlink (`/efi`, say), contemper adds nothing
  and the report says the image's own entry is kept. Comments are
  skipped, escapes decoded and a trailing slash ignored. Only
  `/etc/fstab` is checked: an image's own `boot-efi.mount` or
  `.automount` unit overrides the generated one anyway, and a
  systemd-boot image may additionally get systemd's automatic ESP mount
  at `/efi` if that directory exists and is empty.
- **Opt-outs.** The `io.contemper.fstab="false"` label and
  `convert --no-fstab` that stop the per-volume lines stop this one too.
- **No volume over the ESP.** A declared volume whose path is
  `/boot/efi` or one of its parents (`/boot`, `/`) is a convert error in
  bootloader mode: it would hide the ESP or compete with it for the same
  mount point.
- **UKI mode** adds no line.
- **fsck.** The pass number is 2, but `fsck.vfat` may not be installed in
  the guest. systemd-fsck skips a missing `fsck.vfat`; util-linux and
  BusyBox fsck print a warning and continue. Installing `dosfstools` in
  the image gives the ESP a check at boot.

## Example

`examples/debian-grub` is the reference image for this contract. It
installs Debian's full GRUB packages together with `shim-signed` and the
signed GRUB, so the guest updates in place like a regular Debian
installation, and lays out the ESP by copying files only:

- `EFI/BOOT/BOOT<ARCH>.EFI` is shim, `EFI/BOOT/grub<arch>.efi` the signed
  GRUB it loads and `EFI/BOOT/mm<arch>.efi` the MOK manager;
- the signed GRUB's built-in prefix is `/EFI/debian`, where a stub
  `grub.cfg` finds the root filesystem by label and loads the real
  `/boot/grub/grub.cfg` from it, so the ESP never changes when the kernel
  does.

The image preseeds the two debconf answers described under [In-place
updates](#in-place-updates) and ships a bootstrap `/boot/grub/grub.cfg`
that boots `/vmlinuz` and `/initrd.img`, with a fallback entry for the
previous kernel through `/vmlinuz.old`. Its existence enables Debian's
kernel hook. A oneshot unit runs `grub-install` and `update-grub` on the
first boot, which replaces the bootstrap file with the generated
configuration and leaves the GRUB modules and `core.efi` in place, so the
VM is an ordinary Debian GRUB installation from then on: kernel installs
run `update-grub`, and a GRUB package upgrade reinstalls the bootloader
onto the ESP's fallback path. A snippet in `/etc/default/grub.d/` gives
the generation the serial console and a short timeout.
