# The disk and VM lifecycle

## Layout

Two partitions on a GPT disk, 1 MiB-aligned:

| # | Partition | Contents |
| --- | --- | --- |
| 1 | EFI system partition, 128 MiB, FAT32 | the UKI at `EFI/BOOT/BOOTAA64.EFI` (arm64) or `EFI/BOOT/BOOTX64.EFI` (x86-64) |
| 2 | root, ext4, label `contemper-root` | your merged filesystem, writable |

The UKI sits at the UEFI fallback path for the architecture, so the
firmware finds it without any boot entry being registered on the host.

An image that brings its own bootloader (`io.contemper.boot=bootloader`)
gets a 512 MiB ESP holding its `/boot/efi` tree instead of the UKI; the
root partition is unchanged. See [Bootloader
images](bootloader-images.md), and the
[design page](../design/bootloader-images.md) for the details.

contemper adds `root=LABEL=contemper-root` to the front of the kernel
command line, so the kernel finds the root partition without the image
having to know about the disk layout.

The root comes last, so growing it is the ordinary `growpart` plus
`resize2fs` operation any cloud image uses. No special configuration,
no union filesystem, nothing for your initrd to assemble: the kernel
mounts the root partition and boot is over.

The root partition is sized `max(1 GiB, 1.5 × content + 256 MiB)` by
default. Override it with `--root-size`, for example `--root-size 4GiB`.
Either way the size is rounded up to a whole MiB.

## State behaves the way a container's does

| Container | Your VM | Root filesystem |
| --- | --- | --- |
| `restart` | reboot | kept |
| `stop` / `start` | shutdown / power-on | kept |
| `rm` + `run`, same volumes | replace the instance | reset |
| volume mounts | attached persistent volumes | untouched throughout |

So changes made inside a running VM, including anything you install
with its package manager, survive reboots and shutdowns, and are gone
when you redeploy. Persistent data belongs on an attached volume, where
it survives all of it.

This describes the appliance model, the default with UKI images. A
bootloader image keeps the same mechanics, but its purpose is usually to
stay on one root disk and update in place. See [Long-lived VMs: what
changes](bootloader-images.md#long-lived-vms-what-changes).

This comes from the disk being replaced on redeploy, not from any
layering trick, which is why the root is an ordinary filesystem you can
read, write and grow with ordinary tools.

!!! note "Snapshots and clones"
    A VM's state lives in its disk, so its lifetime follows the disk.
    Snapshotting or cloning a disk copies that state with it, which has
    no clean container analogue; the nearest thing is `docker commit`.

The reasoning behind a plain writable root, rather than a read-only
squashfs with an overlay, is in [Design: root filesystem](../design/root-filesystem.md).
