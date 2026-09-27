# Support image annotations

This page is the schema reference for the annotations a support image
sets on itself. See [Support images](../guide/support-images.md) for how
to use a support image, and [Design: support image
resolution](../design/support-images.md) for why the mechanism looks
this way.

## Where annotations are read

Annotations are read from the support image's **platform manifest** —
the manifest reached after resolving the reference to the platform
`convert` is building for. If the reference names an image index, the
same keys are also accepted on that platform's **descriptor inside the
index**, as a fallback: the manifest wins whenever both set a key.

Only the support image named on the command line is examined this way.
An image pulled in because it won a branch is merged as-is; its own
annotations are never read.

## Names

Branch names and variant names must match:

```text
[a-z0-9][a-z0-9-]*
```

Any annotation key that doesn't parse — an unrecognized shape under
`io.contemper.branch.`, or a branch/variant name outside that pattern —
fails the conversion, naming the offending key.

## Keys

| Key | Meaning |
| --- | --- |
| `io.contemper.requires.files` | comma-separated absolute paths that must exist in the final merged filesystem |
| `io.contemper.branch.<branch>.<variant>.requires.files` | the variant's predicate: comma-separated absolute paths that must **all** exist (AND) for this variant to match |
| `io.contemper.branch.<branch>.<variant>.image` | the image whose layers this variant contributes, resolved for the same platform as everything else. Omitted (or absent) means the variant is a **no-op**: it contributes nothing when it wins |
| `io.contemper.branch.<branch>.default` | the variant applied when no predicate in the branch matches. Must name a variant declared in that branch — a variant that only sets `.image`, or that appears only as this default, both count as declared |

A variant needs a `.requires.files` predicate unless it is the branch's
declared default.

## Resolution

1. Predicates are checked against the **source image's merged filesystem
   only**, before any support layers are merged. A support image can't
   satisfy its own predicates, or a sibling variant's. A path is
   resolved the way a real filesystem would: a symlink anywhere along
   it, not only at the very end, is followed - `/sbin/openrc-init`
   matches equally whether `/sbin` is a real directory or, as on a
   merged-`/usr` distro, a symlink to `/usr/sbin`.
2. Each branch resolves independently:
   - Exactly one variant matches → that variant wins.
   - No variant matches → the branch's declared default wins; if none is
     declared, the conversion fails, naming the branch and, for each
     variant with a predicate, the paths checked and which were missing.
   - Two or more variants match → the conversion fails, naming the
     branch and the matching variants, even if a default is declared.
3. Only the winning variants' images are fetched — manifest first, then
   layers. A losing variant's manifest is never requested.
4. Resolution is exactly one level deep: a variant image's own
   annotations are never read, so a variant image cannot declare further
   branches of its own.
5. The final filesystem is the source image's layers, then the support
   image's own layers, then each winning variant's layers, **in branches
   sorted by name**. This order is fixed regardless of annotation order,
   so it doesn't depend on map iteration or how the annotations happened
   to be written.

There is no priority ordering and no expression language: AND is
"require several paths in one variant", OR is "declare two variants in
the same branch". If two variants can both match at once, their
predicates are wrong for that axis.

## Worked example

A support image with two independent branches: `init-system`, choosing
the service definition to install for whichever init system is present,
and `first-boot`, adding configuration only when a day-1 provisioning
tool is installed.

```text
io.contemper.branch.init-system.openrc.requires.files=/sbin/openrc-init
io.contemper.branch.init-system.openrc.image=ghcr.io/example/support-openrc:v5
io.contemper.branch.init-system.systemd.requires.files=/usr/lib/systemd/systemd
io.contemper.branch.init-system.systemd.image=ghcr.io/example/support-systemd:v5

io.contemper.branch.first-boot.cloud-init.requires.files=/usr/bin/cloud-init
io.contemper.branch.first-boot.cloud-init.image=ghcr.io/example/support-cloud-init:v5
io.contemper.branch.first-boot.default=none
```

Against an image whose merged filesystem has `/sbin/openrc-init` but no
systemd and no cloud-init:

- `init-system` has exactly one match (`openrc`) → its image is fetched
  and merged.
- `first-boot` has zero matches, but declares `none` as its default.
  `none` is never given a `.image`, so it's a no-op: the branch resolves
  to a name for reporting purposes, and nothing is fetched or merged for
  it.

Layers merge in this order: the source image, the support image's own
layers, then `first-boot` before `init-system` (branches sorted by
name) — here that's nothing (the no-op default), then the `openrc`
image's layers.
