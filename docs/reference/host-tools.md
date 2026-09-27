# Host tools

contemper is a single Go binary plus a few host tools it discovers
rather than bundles. It looks on `PATH` first, then in Homebrew's
keg-only locations (e2fsprogs isn't linked onto `PATH` by default), and
prints an install hint when something is missing.

The tools are deliberately not bundled or downloaded: the copies your
distribution or Homebrew installs get their security fixes from there,
on your schedule, and contemper itself stays one static binary.

| Tool | Package | Used for |
| --- | --- | --- |
| `mkfs.ext4`, `debugfs`, `e2fsck` | e2fsprogs | creating, filling and checking the root filesystem |
| `qemu-img` | qemu (Homebrew), `qemu-utils` (Debian/Ubuntu) | converting the raw disk to qcow2 |
| `qemu-system-aarch64`, `qemu-system-x86_64` | qemu, `qemu-system-*` | `deploy --to local-qemu` only |
| UEFI firmware | included with Homebrew's qemu; `ovmf`, `qemu-efi-aarch64` or AAVMF on Linux | `deploy --to local-qemu` only |

These are the only programs contemper runs. Nothing from a converted
image is ever executed on the host. Tools are run directly, never
through a shell. Their output is shown when they fail, and with
`--verbose` each invocation is printed.

No container runtime is needed to convert. You need one only to build
images in the first place.
