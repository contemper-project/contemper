# Host tools

contemper is a single Go binary plus a few host tools it discovers
rather than bundles. It looks on `PATH` first, then in Homebrew's
keg-only locations (e2fsprogs isn't linked onto `PATH` by default), and
prints an install hint when something is missing. None of them has a
minimum version contemper requires; whatever your distribution or
Homebrew currently ships is fine.

The tools are deliberately not bundled or downloaded: the copies your
distribution or Homebrew installs get their security fixes from there,
on your schedule, and contemper itself stays one static binary.

The root filesystem's ext4 features don't depend on the version you have:
contemper hands `mkfs.ext4` its own configuration (see
[How it works](../guide/how-it-works.md#building-the-disk-without-root)).

## Needed to convert

`convert` always needs these, regardless of target:

| Tool | Package | Used for |
| --- | --- | --- |
| `mkfs.ext4`, `debugfs`, `e2fsck` | `e2fsprogs` (all platforms) | creating, filling and checking the root filesystem |
| `qemu-img` | `qemu` (Homebrew); `qemu-utils` (Debian/Ubuntu); `qemu-img` (Fedora/RHEL) | converting the raw disk to qcow2 |

## Needed only for `deploy --to local-qemu`

Everything else is only for booting a bundle locally, and only the
architecture of the bundle you're deploying: an aarch64 host deploying an
amd64 bundle (or the reverse) needs the *other* architecture's emulator
and firmware, not its own.

| Tool | Package | Architecture |
| --- | --- | --- |
| `qemu-system-x86_64` | `qemu` (Homebrew); `qemu-system-x86` (Debian/Ubuntu); `qemu-system-x86-core` (Fedora/RHEL) | amd64 bundles |
| `qemu-system-aarch64` | `qemu` (Homebrew); `qemu-system-arm` (Debian/Ubuntu); `qemu-system-aarch64-core` (Fedora/RHEL) | arm64 bundles |
| UEFI firmware | included with Homebrew's `qemu`; `ovmf` (Debian/Ubuntu); pulled in by `qemu-system-x86-core` (Fedora/RHEL) | amd64 bundles |
| UEFI firmware | included with Homebrew's `qemu`; `qemu-efi-aarch64` (Debian/Ubuntu); pulled in by `qemu-system-aarch64-core` (Fedora/RHEL) | arm64 bundles |

On Fedora/RHEL the `-core` emulator subpackages already require their
matching firmware package (`edk2-ovmf`, `edk2-aarch64`), so installing
one of them is enough to get both; the plain `qemu-system-x86`/
`qemu-system-aarch64` packages pull in the same emulator plus a full set
of GUI backends contemper's headless boot never uses.

The Homebrew cask and the `.deb`/`.rpm` packages (see
[Getting started](../getting-started.md#install)) install `e2fsprogs` and
a `qemu-img` binary as hard dependencies, since `convert` always needs
them. The emulator and firmware above are only suggested, not installed:
which ones you need depends on the architecture of the bundles you
deploy, not on the package.

## Hardware acceleration for `deploy --to local-qemu`

`deploy --to local-qemu` uses hardware acceleration when it can, and
falls back to (much slower) software emulation otherwise:

- Deploying a bundle for the host's own architecture: HVF on macOS; on
  Linux, KVM if `/dev/kvm` can be opened for read/write (not merely
  present — a CI runner can have the device node without granting access
  to it). Being able to open `/dev/kvm` usually means being a member of
  the `kvm` group, or already having the right ACL from an existing
  session.
- Deploying a bundle for a *different* architecture than the host's own
  always falls back to software emulation (TCG): neither HVF nor KVM can
  run a foreign architecture's instructions. Under TCG, arm64 guests use
  the `neoverse-n1` CPU model and amd64 guests use `max`.

No extra package installs this: `/dev/kvm` comes from the kernel, and
Homebrew's qemu on Apple Silicon/Intel Macs already includes HVF support.

Nothing from a converted image is ever executed on the host. Tools are
run directly, never through a shell. Their output is shown when they
fail, and with `--verbose` each invocation is printed.

No container runtime is needed to convert a plain registry, archive or
layout reference. You need one only to build images in the first place,
and `docker` specifically for `contemper build` or a `docker-daemon:`
source: both run `docker` (`buildx` too, for `build`) as a subprocess,
the same way as the tools above.
