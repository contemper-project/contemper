# Roadmap

What contemper ships today is in the
[changelog](https://github.com/contemper-project/contemper/blob/main/CHANGELOG.md)
and the rest of these docs, starting with [how conversion
works](../guide/how-it-works.md) and [deploying locally](../guide/deploying.md).
Open work is tracked on GitHub:

## Next

Planned work lives in the
[contemper project](https://github.com/orgs/contemper-project/projects/1),
grouped into [milestones](https://github.com/contemper-project/contemper/milestones)
by theme rather than by version - see
[Planning and priorities](https://github.com/contemper-project/contemper/blob/main/CONTRIBUTING.md#planning-and-priorities)
in CONTRIBUTING.md for how issues, priority, and milestone order work,
and what's next.

Images that ship their own EFI bootloader (including Secure Boot) and
multi-arch conversion have shipped; see [bootloader
images](../guide/bootloader-images.md) and [multi-arch
builds](../guide/multi-arch.md).

The themes currently open as milestones:

- **Podman builds:** building with podman, and a `containers-storage:`
  source.
- **Incus:** publish and deploy
  against a remote Incus, Incus volumes, and deployment metadata in the
  bundle manifest.
- **Root filesystem:** faster population with `mkfs.ext4 -d`, keeping
  hardlinks, long paths, and a configurable `root=`.
