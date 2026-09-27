# Targets

A target names what `convert` builds: the platform, and implicitly the
output format.

| Target | Canonical name | Output | Default support image |
| --- | --- | --- | --- |
| `qemu` | `qemu-qcow2` | qcow2, UEFI-bootable UKI | none |
| `incus` | `incus-qcow2` | qcow2, UEFI-bootable UKI, plus the Incus support image | none yet (planned) |

`qemu` is the reference target. It adds nothing to your image, and the
disk boots under any QEMU with UEFI firmware and nothing else. It is the
debugging baseline, and what `deploy --to local-qemu` is for.

`incus` builds the same disk format, since Incus runs VMs on QEMU with
OVMF firmware. It differs only in the support image layered on top.

!!! warning "Planned"
    The Incus support image (agent, service definitions, boot
    configuration) is not published yet, so `incus` has no default
    support image and currently produces the same disk as `qemu`. Once
    it ships, it will be `ghcr.io/contemper-project/incus-support`,
    following the naming convention in [Support
    images](support-images.md#naming).

## Default support images

Each target may declare a **default support image**: the reference used
when `--target` alone is given, with no `--support`. It's how `incus`
will eventually mean "qcow2 plus the Incus agent" without you having to
name the image yourself.

`--support <ref>` **replaces** the target's default outright; it never
stacks with it. There is no flag to merge both an explicit support image
and the target's default in one build - a target's own support image is
free to declare variants for the things it needs to adapt to (see
[Support images](support-images.md)), which covers the common case of
wanting the default *plus* something conditional.

The bundle manifest and the progress output both record which one
applied: `contemper.json`'s `support.origin` is `"target"` when the
default was used, `"flag"` when `--support` replaced it. See [Bundle
manifest](../reference/bundle.md).

## Names and aliases

Where a platform needs more than one format, it's a suffix on the target
name, `incus-qcow2` vs. `incus-raw`, in the spirit of Packer's builder
naming. The unsuffixed name is an alias for that platform's default. One
flat namespace, rather than platform and format as independent axes,
avoids a large surface of mostly meaningless combinations.

Both spellings work. The suffixed one is canonical, and it's what the
progress output and the bundle manifest record: `--target incus` is
reported as `incus-qcow2`. A platform's default format won't change
under you, since that would silently alter what your pipeline produces,
so it's treated as a breaking change.

## Targets and providers

A *target* is what `convert` builds: which support image went in, and
which format came out. A *provider* is where a bundle goes afterwards:
`local-qemu`, an Incus server. They are separate on purpose. An
`incus-qcow2` bundle boots fine under `local-qemu`; the target records
what went *into* the disk, not where it may run.
