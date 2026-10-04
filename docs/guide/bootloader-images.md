# Bootloader images

By default contemper boots your image with a Unified Kernel Image (UKI)
it assembles itself. An image can instead bring its own EFI bootloader
(GRUB, systemd-boot, ...) and keep its kernel updatable in place. This
page helps you choose, and shows how to build such an image. The
[design page](../design/bootloader-images.md) is the reference for the
details.

## Choosing a boot mode

With bootloader images contemper supports two ways of using a VM:

- **Appliance.** The image is the unit of change. You upgrade by
  redeploying a new image version on the same volumes, like a
  container. UKI images are made for this.
- **Long-lived VM.** The image only sets up the VM's first deploy. From
  then on the guest maintains itself with its package manager, like a VM
  image from other tooling. This needs a bootloader image.

| | UKI (default) | Bootloader |
| --- | --- | --- |
| Usual model | appliance | long-lived VM (works as an appliance too) |
| Kernel, initrd, command line | sealed into the boot image at build time | read from disk by the bootloader at every boot |
| Updating the kernel | build a new image, replace the disk | the guest's package manager, in place |
| Root disk | disposable; matches the image | can stay for the VM's life; drifts from the image |
| ESP | 128 MiB, holds the UKI | 512 MiB, holds your `/boot/efi` tree |
| Who writes the boot setup | contemper | you |

**Pick UKI** for an appliance: the image is the unit of change, builds
are reproducible, and state that must survive an update lives on
[volumes](volumes.md). This is the model most images want.

**Pick a bootloader** when the VM should behave like a regular
installation that updates its own kernel with `apt` or `dnf`, or when
the image has no volumes to hold its state.

!!! warning "A UKI image cannot update its kernel in the guest"
    The kernel, initrd and command line are sealed into the UKI when
    the bundle is built. A kernel package installed inside the guest
    lands on the root filesystem, but nothing boots it: the next boot
    runs the UKI as it was. That is why a long-lived VM needs
    bootloader mode.

The cost of a bootloader image is the one the
[disk lifecycle](disk.md) already describes. Changes made inside the
guest, kernel updates included, survive reboots and shutdowns of that
disk, but they are not part of the image. Redeploying a new image
version replaces the root disk with a fresh one, and those changes are
gone. Volumes are untouched by a redeploy and keep their data, so
persistent data still belongs there.

## Long-lived VMs: what changes

In the long-lived model the running VM, not the image, is the source of
truth after the first deploy. These features behave differently, or
don't fit:

- **Redeploy.** It replaces the root disk and discards every in-place
  update. A new image version means a new VM, not an upgrade of the
  existing one. Don't mix the two upgrade paths on one VM: pick the
  package manager or redeploys.
- **Root size.** The default, `max(1 GiB, 1.5 × content + 256 MiB)`,
  leaves little room to grow, and every kernel with its initrd, plus
  package caches, uses it up. Size it up front with `--root-size` or
  the `io.contemper.root.size` label (the flag wins when both are
  given; the label is capped at 16 GiB, the flag is not). Or grow it later with `growpart` and `resize2fs`, since the
  root partition is last; see [the disk](disk.md#layout).
- **Volumes.** They are optional here, but still useful to keep data
  apart from the OS, or to move it to a rebuilt VM. Seeding happens only
  when a volume is first formatted, so content a later image has at a
  volume path never reaches an existing volume, as with containers; see
  [Seeding a volume from the image](volumes.md#seeding-a-volume-from-the-image).
- **Image-time settings.** The fstab lines for volumes and the ESP, and
  anything else `convert` writes into the root, are written once. After
  that they are ordinary guest files, and a later image that changes
  them does not reach an existing VM.
- **The image as a record.** The source digest in the bundle manifest,
  image scans and `deploy --expect` boot tests describe the VM at its
  first deploy only. The running VM drifts from them.
- **local-qemu.** The target boots with `snapshot=on`, so it is a test
  target, not a place to run a long-lived VM; see the note below.

!!! note "With `deploy --to local-qemu`"
    The local-qemu target boots the disk with `snapshot=on`: writes go
    to a throwaway overlay that lasts as long as the QEMU process. A
    guest reboot keeps its changes, but every new `deploy` starts from
    the bundle as converted, so in-place updates last for one deploy
    only. Volumes do persist; see [Volumes](volumes.md).

## Making a bootloader image

Declare the mode with a label:

```dockerfile
LABEL io.contemper.ready="true" \
      io.contemper.boot="bootloader"
```

Then provide, in the image:

- **The EFI binary** at `/boot/efi/EFI/BOOT/BOOTX64.EFI` (amd64) or
  `/boot/efi/EFI/BOOT/BOOTAA64.EFI` (arm64). A fresh VM has no boot
  entries, so the firmware can only find this fallback path. Anything
  else under `/boot/efi` is copied onto the ESP as well.
- **A configuration that finds the root by its label**,
  `contemper-root`: `root=LABEL=contemper-root` on the kernel command
  line, `search --label contemper-root` in GRUB. Your initrd must be
  able to resolve `LABEL=`, as described in
  [Authoring an image](authoring.md#the-fixed-path-contract).
- **A kernel and initrd** where your bootloader looks for them. The
  `/boot/contemper/` files are not used in this mode; if present,
  `convert` reports that they are ignored.

contemper does not generate or edit any bootloader configuration, and
it does not install a bootloader. Your image ships it already installed
under `/boot/efi`.

### Example: Debian with GRUB

`examples/debian-grub/Containerfile` is the reference. It gives a VM that
updates in place like a regular Debian installation: a kernel update
regenerates the GRUB configuration and a GRUB package update reinstalls
the bootloader. The points that matter:

- **Packages.** `grub-efi-<arch>`, `grub-efi-<arch>-signed` and
  `shim-signed` go in next to the kernel. They install fine in a
  container build, because the GRUB postinst skips `grub-install` there
  and the kernel hook skips itself in a container.
- **Debconf preseed**, before the install:
  `grub2/force_efi_extra_removable` true and `grub2/update_nvram` false.
  A fresh VM has no boot entries, so every `grub-install` inside the
  guest must keep the fallback path current and leave NVRAM alone.
- **Files copied onto the ESP.** `update-grub` and `grub-install` fail
  in a container build (they probe the root device), so the image
  copies the ready-made binaries instead of installing them: shim to
  `EFI/BOOT/BOOTX64.EFI` (arm64: `BOOTAA64.EFI`), the signed GRUB to
  `EFI/BOOT/grubx64.efi` (`grubaa64.efi`), which shim loads from its own
  directory, and the MOK manager to `EFI/BOOT/mmx64.efi`
  (`mmaa64.efi`). shim's `fbx64.efi` is left out: it writes boot entries
  and resets, which a VM without persistent NVRAM would do on every
  boot.
- **A stub `EFI/debian/grub.cfg`.** The signed GRUB has `/EFI/debian`
  built in as its prefix. The stub finds the root filesystem by label
  and loads the real `/boot/grub/grub.cfg` from it, so the ESP never
  needs to change when the kernel does.
- **`/etc/default/grub.d/contemper.cfg`** sets the serial console on the
  kernel command line, a one second menu timeout and
  `GRUB_TERMINAL="console serial"`. `update-grub` applies it in the
  guest.
- **A bootstrap `/boot/grub/grub.cfg`** boots `/vmlinuz` and
  `/initrd.img` with `root=LABEL=contemper-root`, plus the previous
  kernel through `/vmlinuz.old` as a way back from a bad update. It is
  only used for the very first boot. Its existence also makes the kernel
  hook run at all.
- **A first-boot unit**, `contemper-grub-firstboot.service`, runs
  `grub-install --target=<x86_64-efi|arm64-efi> --force-extra-removable
  --no-nvram` and then `update-grub` once the ESP is mounted, before the
  boot marker. Without it two things are missing in the guest. The GRUB
  modules in `/boot/grub/<target>/` do not exist until a `grub-install`,
  yet Debian's generated `grub.cfg` loads them (`insmod bli`), so every
  boot would print `error: file '/boot/grub/<target>/bli.mod' not
  found`. And the `grub-efi-<arch>` postinst only reruns `grub-install`
  on a package upgrade if `/boot/grub/<target>/core.efi` exists, which
  `grub-install` creates, so a GRUB upgrade would never refresh the ESP.
  The unit writes a stamp file (`/var/lib/contemper-grub-firstboot/done`)
  only after both commands succeeded and is skipped once it exists, so a
  run that failed part-way is retried on the next boot and meanwhile
  shows up as a failed unit. It is ordered before the boot marker, which
  does not mean it succeeded.

What happens in the guest: on the first boot the unit installs GRUB and
`update-grub` replaces the bootstrap `grub.cfg` with the generated one
(with `root=UUID=` of the real filesystem, which only exists once
`convert` has created the disk). From then on the VM is an ordinary
Debian GRUB installation: every `apt` kernel install or removal runs the
hook, which runs `update-grub`, and a GRUB package upgrade runs
`grub-install` to the fallback path without touching NVRAM.

### Other bootloaders

- **systemd-boot** keeps kernels on the ESP, which is why the ESP is
  512 MiB: updates need room. Ship the `loader/` entries and kernels
  under `/boot/efi`.
- Mind what FAT can store. Only regular files and directories are
  allowed in the `/boot/efi` tree, names are restricted, and some names
  that differ only slightly collide on FAT. The one you are most likely
  to meet is a `<machine-id>/<kver>/` layout with two Debian- or
  Ubuntu-style kernel versions (`6.1.0-25-amd64`, `6.1.0-26-amd64`) in
  one directory, which `convert` rejects for now. The complete rules are
  under [What can go onto
  FAT](../design/bootloader-images.md#what-goes-where).

## What convert checks

`convert` checks that the label values are valid, that `/boot/efi` is a
directory, and that the fallback file exists and is a PE32+ EFI
application for the target architecture. It also checks the FAT rules
above and that the tree fits the ESP. `/sbin/init` is not required,
since your bootloader configuration may pass `init=`; `convert` warns if
it does not resolve. With `io.contemper.secure-boot="true"` it also
checks that the fallback file carries a signature.

It does not check the bootloader's configuration, or that the kernels it
will look for exist. A wrong configuration shows up at boot. Boot the
bundle with `deploy --expect` to catch it before you ship; see
[Deploying locally](deploying.md#boot-tests). The full list is in
[What convert checks](../design/bootloader-images.md#what-convert-checks).

## The ESP in the guest

For in-guest updates to reach the ESP it must be mounted at
`/boot/efi`. In bootloader mode `convert` appends this line to the
image's `/etc/fstab`:

```text
LABEL=ESP /boot/efi vfat umask=0077 0 2
```

- If the image's fstab already has an entry for `/boot/efi` (or for the
  directory it resolves to, when it is a symlink such as `/efi`),
  contemper leaves it alone and says so.
- `io.contemper.fstab="false"` and `convert --no-fstab` turn the line
  off, along with the per-volume lines.
- There is no `nofail`: the ESP is on the boot disk, and a corrupt one
  stops boot in emergency mode, as on a regular installation. Install
  `dosfstools` in the image if you want the ESP checked at boot.

## Volumes

Volumes work as in UKI mode. One restriction applies: a volume at
`/boot/efi`, or at one of its parents (`/boot`, `/`), is a `convert`
error in bootloader mode, because it would hide the ESP or compete with
it for the mount point.

## Secure Boot

An image may ship shim and a signed bootloader at the fallback path; the
Debian example does. With Secure Boot disabled, shim simply loads the
bootloader.

To boot with Secure Boot enabled, declare it in the image. For the
Debian example, add one label:

```dockerfile
LABEL io.contemper.ready="true" \
      io.contemper.boot="bootloader" \
      io.contemper.secure-boot="true"
```

`convert` then checks that the fallback binary carries a signature (the
example's shim does) and records the request in the manifest. It does
not verify the signature's chain, so a bootloader signed with a key the
firmware does not trust fails at boot, not at convert. The label is only
valid on bootloader images: contemper's UKI is unsigned. Locally,
`deploy --to local-qemu` then needs Secure Boot firmware with Microsoft's
keys enrolled; see [Deploying locally](deploying.md) and the
[design](../design/bootloader-images.md#secure-boot).

With Secure Boot on, GRUB prints `error: prohibited by secure boot policy.`
for the modules Debian's generated configuration loads from disk (such as
`bli`). The signed GRUB cannot load them, and booting continues.
