# Distributions

contemper works with any distribution whose image satisfies the
[image contract](authoring.md). This page collects what each family
needs to meet it, and lists the distributions that are always supposed
to work.

## Covered distributions

These entries are exercised by a scheduled boot test suite. They are
what is always supposed to work. A failure of the suite opens an issue
automatically.

| Distribution | Entry | Architectures | Since | Family |
| --- | --- | --- | --- | --- |
| Alpine Linux 3.23 | `alpine-3.23` | amd64 | 0.3.0 | OpenRC, mkinitfs |
| Alpine Linux 3.24 | `alpine-3.24` | amd64, arm64 | 0.3.0 | OpenRC, mkinitfs; newest stable |
| Alpine Linux edge | `alpine-edge` | amd64 | 0.3.0 | OpenRC, mkinitfs; rolling |
| Debian 12 | `debian-12` | amd64 | 0.3.0 | systemd, initramfs-tools |
| Debian 13 | `debian-13` | amd64, arm64 | 0.3.0 | systemd, initramfs-tools; newest stable |
| Debian testing | `debian-testing` | amd64 | 0.3.0 | systemd, initramfs-tools; development |
| Ubuntu 22.04 LTS | `ubuntu-22.04` | amd64 | 0.3.0 | systemd, initramfs-tools |
| Ubuntu 24.04 LTS | `ubuntu-24.04` | amd64 | 0.3.0 | systemd, initramfs-tools |
| Ubuntu 26.04 LTS | `ubuntu-26.04` | amd64, arm64 | 0.3.0 | systemd, initramfs-tools; newest |
| Fedora 43 | `fedora-43` | amd64 | 0.3.0 | systemd, dracut |
| Fedora 44 | `fedora-44` | amd64, arm64 | 0.3.0 | systemd, dracut; newest |
| Fedora Rawhide | `fedora-rawhide` | amd64 | 0.3.0 | systemd, dracut; development |
| AlmaLinux 9 | `almalinux-9` | amd64 | 0.3.0 | systemd, dracut |
| AlmaLinux 10 | `almalinux-10` | amd64, arm64 | 0.3.0 | systemd, dracut; newest |
| Rocky Linux 9 | `rockylinux-9` | amd64 | 0.3.0 | systemd, dracut |
| Rocky Linux 10 | `rockylinux-10` | amd64 | 0.3.0 | systemd, dracut; newest |
| CentOS Stream 10 | `centos-stream-10` | amd64 | 0.3.0 | systemd, dracut |
| openSUSE Tumbleweed | `opensuse-tumbleweed` | amd64, arm64 | 0.3.0 | systemd, dracut; rolling |
| openSUSE Leap 16.0 | `opensuse-leap-16` | amd64 | 0.3.0 | systemd, dracut |
| Arch Linux | `arch-latest` | amd64 | 0.3.0 | systemd, mkinitcpio; rolling |

Open problems with these and other target distributions are tracked as
[issues labelled `area: target-distros`](https://github.com/contemper-project/contemper/issues?q=is%3Aissue+is%3Aopen+label%3A%22area%3A+target-distros%22).

"Since" is the contemper release the entry first appears in. The
architectures are the ones the entry is covered on, which can be fewer
than the distribution publishes.

### Derivatives

A derivative is expected to behave like its base when it shares the
base's kernel packaging, initrd generator and init system. Only the
entries in the table are covered, though. If a derivative differs in one
of those three, check the base's section below and adapt.

### How distributions are chosen

- Families that differ in kernel packaging, initrd tool or init system
  each get an entry.
- A derivative gets an entry only if it is popular, publishes an
  official container image, and diverges from its base in one of those
  three traits.
- Versions: the oldest release that is still supported, the newest, and
  a rolling or development release where the distribution has one.
- arm64: one entry per family, where the family's base image is
  published for arm64.
- An entry is removed when its release reaches end of life.
- An entry that doesn't work yet stays out of the table. It gets its own
  issue, labelled `area: target-distros`.

To add an entry, see
[Contributing](https://github.com/contemper-project/contemper/blob/main/CONTRIBUTING.md).

## Per-family notes

Each family has a worked example, a Containerfile under
`test/distros/`, linked in its section. They differ from the examples in
`examples/` mainly in taking the base image as a build argument.

Everything below is a consequence of building inside a container. The
base images are minimal, and package scriptlets assume a running machine.
Some points apply to every family:

- The serial console is `ttyS0` on amd64 and `ttyAMA0` on arm64. Put it
  on the kernel command line (`/boot/contemper/cmdline`) to see the
  boot.
- Generate the initrd in generic mode, so it doesn't depend on the
  build host. The setting differs per tool and is given below.
- Write `LABEL=contemper-root / ext4 rw,relatime 0 1` to `/etc/fstab`.
  contemper labels the root filesystem `contemper-root` and puts
  `root=LABEL=contemper-root` first on the command line.
- Install the udev (or mdev) implementation explicitly. The initrd
  resolves `root=LABEL=` through the by-label links it creates.
- Install `e2fsprogs`, so the initrd and the booted system have
  `fsck.ext4`.
- Without a machine-id, locale, timezone and hostname, systemd's first
  boot can stop at a prompt on the console. The test images mask
  `systemd-firstboot.service` to stay unattended. Configure these in
  your image instead if you need them.
- The container images have no root password. The test images give root
  an empty password for console access; do what suits your image.

### Alpine

- Init system: OpenRC. Kernel: `linux-virt`, at `/boot/vmlinuz-virt`;
  link it to `/boot/contemper/vmlinuz`.
- Initrd: `mkinitfs`. It has no host-only mode. It includes only the
  features named with `-F`, so `-F "base ext4 virtio scsi"` is already
  generic.
- Also install `busybox-mdev-openrc` (the mdev service, which `openrc`
  alone doesn't provide) and `e2fsprogs`.
- Add `rootfstype=ext4 modules=ext4` to the command line.
- The `fsck` OpenRC service is not enabled by default. Run
  `rc-update add fsck boot`, or the root filesystem is never checked at
  boot. The other services a minimal system needs (`devfs`, `dmesg`,
  `mdev`, `hwdrivers`, `modules`, `sysctl`, `hostname`, `bootmisc`) are
  also not enabled for you.
- For a login on the console, add a getty line for it to
  `/etc/inittab`.

Example:
[`test/distros/alpine/Containerfile`](https://github.com/contemper-project/contemper/tree/main/test/distros/alpine/Containerfile).

### Debian and Ubuntu

- Init system: systemd. Install `systemd-sysv`; the Ubuntu container
  image ships neither systemd nor a kernel.
- Kernel: `linux-image-<arch>` on Debian (`linux-image-amd64`,
  `linux-image-arm64`) and `linux-image-virtual` on Ubuntu. The kernel is
  `/boot/vmlinuz-<kver>`; link it to `/boot/contemper/vmlinuz`.
- Initrd: `initramfs-tools`. Its default `MODULES=most` is already
  generic, so no flag is needed. Use `mkinitramfs -o
  /boot/contemper/initrd <kver>`, which writes to a fixed path;
  `update-initramfs` doesn't.
- Install `udev` explicitly. With `--no-install-recommends` it isn't
  pulled in, and the initrd can't resolve `root=LABEL=` without it.
- **fsck can be missing from the initrd.** The `initramfs-tools` fsck
  hook copies `fsck` and `fsck.<type>` into the initrd only for the
  filesystem types it finds for `/` in `/etc/fstab`. In a container
  build the fstab entry doesn't exist yet when `mkinitramfs` runs, and
  `LABEL=` can't be resolved to a type, so the boot silently skips the
  root fsck ("fsck not present"). Add `FSTYPE=ext4` to
  `/etc/initramfs-tools/initramfs.conf` before generating the initrd.
- Ubuntu 22.04's initrd carries e2fsprogs 1.46. See
  [the ext4 feature set](#ext4-features-and-older-guests).

Example:
[`test/distros/debian/Containerfile`](https://github.com/contemper-project/contemper/tree/main/test/distros/debian/Containerfile),
used for both Debian and Ubuntu.

### Fedora

- Init system: systemd. The container image doesn't include it; install
  `systemd` and `systemd-udev`.
- Kernel: `kernel-core`, which has the kernel and the modules needed to
  boot (ext4, virtio). The kernel image is
  `/usr/lib/modules/<kver>/vmlinuz`, not under `/boot`.
- The kernel package's scriptlets run `kernel-install`, which has no
  machine-id or boot loader to work with in a container build. Ignore
  the result and generate the initrd yourself.
- Initrd: `dracut`, which is host-only by default. Pass
  `--no-hostonly --no-hostonly-cmdline`, and `--force` to replace one
  `kernel-install` left behind.
- Also install `dbus-broker` (systemd-logind fails without a D-Bus
  socket), `e2fsprogs`, `kmod` and `util-linux`.
- Install with `--setopt=install_weak_deps=False` to keep the image
  small.
- The container image has no `passwd`; edit the root entry in
  `/etc/shadow` to clear its password.

Example:
[`test/distros/fedora/Containerfile`](https://github.com/contemper-project/contemper/tree/main/test/distros/fedora/Containerfile).

### Enterprise Linux

AlmaLinux, Rocky Linux and CentOS Stream. They share packaging and boot
tooling, so one Containerfile serves all of them.

- Init system: systemd, kernel and initrd as for Fedora: `kernel-core`,
  the image at `/usr/lib/modules/<kver>/vmlinuz`, and
  `dracut --no-hostonly --no-hostonly-cmdline`.
- The container images are minimal. Install `systemd`, `systemd-udev`,
  `e2fsprogs`, `kmod` and `util-linux`.
- Base images: `almalinux`, `rockylinux/rockylinux` (the `rockylinux`
  library image is stale) and `quay.io/centos/centos:stream10`.
- As on Fedora, clear the root password in `/etc/shadow` and mask
  `systemd-firstboot.service`.
- Enterprise Linux 9's initrd carries e2fsprogs 1.46. See
  [the ext4 feature set](#ext4-features-and-older-guests).

Example:
[`test/distros/el/Containerfile`](https://github.com/contemper-project/contemper/tree/main/test/distros/el/Containerfile).

### openSUSE

- Init system: systemd, which, like udev, is not part of the container
  image. Install `systemd` and `udev`.
- Kernel: `kernel-default`. The leaner `kernel-kvmsmall` lacks some
  drivers. The kernel image is `vmlinuz` on amd64 but `Image` on arm64,
  under `/usr/lib/modules/<kver>/` or as `/boot/vmlinuz-<kver>` /
  `/boot/Image-<kver>`; check which exists before linking it. The arm64
  `Image` carries the EFI stub's PE header and can be used as is.
- The kernel package's scriptlets try to build an initrd for the build
  host. Generate your own with
  `dracut --no-hostonly --no-hostonly-cmdline --force`.
- Install with `zypper install --no-recommends` to keep the image small,
  and add `dracut`, `e2fsprogs`, `kmod` and `util-linux`.
- Clear the root password in `/etc/shadow` and mask
  `systemd-firstboot.service`.

Example:
[`test/distros/opensuse/Containerfile`](https://github.com/contemper-project/contemper/tree/main/test/distros/opensuse/Containerfile).

### Arch

- Init system: systemd. Kernel: `linux`, which ships no copy under
  `/boot`. The kernel is `/usr/lib/modules/<kver>/vmlinuz`.
- Initrd: `mkinitcpio`. Its default `HOOKS` include `autodetect`, which
  trims the initrd to the build host's modules. Remove it, for example
  `HOOKS=(base udev modconf block filesystems fsck)`, then run
  `mkinitcpio -k <kver> -g /boot/contemper/initrd`.
- Install with `pacman -Syu`. Arch doesn't support a partially synced
  package database, so the image must be fully upgraded when you install.
- Make sure exactly one kernel is installed, so that the version you
  pick is the right one.
- The official `archlinux` image is published for amd64 only.
- Mask `systemd-firstboot.service`, as above.

Example:
[`test/distros/arch/Containerfile`](https://github.com/contemper-project/contemper/tree/main/test/distros/arch/Containerfile).

### ext4 features and older guests

contemper creates the root filesystem with a fixed ext4 feature set
rather than the host's defaults, as described in
[How conversion works](how-it-works.md#building-the-disk-without-root).
Older guests are the reason. An initrd with an old `e2fsck` (1.46 on
Enterprise Linux 9 and Ubuntu 22.04) refuses to check a filesystem with
features such as `orphan_file`, and boot stops at the root fsck. Nothing
needs doing in your image.
