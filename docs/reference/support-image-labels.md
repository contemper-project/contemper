# Support image labels

This page is the schema reference for the labels a support image sets on
itself. See [Support images](../guide/support-images.md) for how to use
a support image, and [Design: support image
resolution](../design/support-images.md) for why the mechanism looks
this way.

## Where labels are read

The keys below are read from the `Config.Labels` of the support image's
**platform image config**: the config reached after resolving the
reference to the platform `convert` is building for. In a Containerfile
that is the `LABEL` instruction, so a support image needs nothing beyond
the tooling that builds any other image. A support image built `FROM`
another one inherits its labels and can add or override them.

Images published with these keys as manifest annotations or index
descriptor annotations are still read for now: the sources are merged key
by key, and for a key set in several places the label wins, then the
manifest annotation, then the index annotation.

Only the support image named on the command line is examined this way.
An image pulled in because it won a branch is merged as-is; its own
labels are never read.

## Names

Branch names and variant names must match:

```text
[a-z0-9][a-z0-9-]*
```

Any label key that doesn't parse — an unrecognized shape under
`io.contemper.branch.`, or a branch/variant name outside that pattern —
fails the conversion, naming the offending key.

## Keys

| Label | Meaning |
| --- | --- |
| `io.contemper.requires.files` | comma-separated absolute paths that must exist in the final merged filesystem |
| `io.contemper.branch.<branch>.<variant>.requires.files` | the variant's predicate: comma-separated absolute paths that must **all** exist (AND) for this variant to match |
| `io.contemper.branch.<branch>.<variant>.image` | the image whose layers this variant contributes, resolved for the same platform as everything else. Omitted (or absent) means the variant is a **no-op**: it contributes nothing when it wins |
| `io.contemper.branch.<branch>.default` | the variant applied when no predicate in the branch matches. Must name a variant declared in that branch — a variant that only sets `.image`, or that appears only as this default, both count as declared |

A variant needs a `.requires.files` predicate unless it is the branch's
declared default.

Where a variant's `.image` may point, and with what credentials it is
pulled, depends on where the support image itself came from:

- **Support image from a registry:** `.image` must be a registry
  reference. It may name any registry and namespace, but:
    - a variant in the **same registry and namespace** as the support
      image (the first path component, such as `acme` in
      `ghcr.io/acme/support`; the repository may differ; `docker.io` and
      `index.docker.io` count as the same registry) is pulled with your
      registry credentials, so a private support image can have private
      variants. A support image whose repository has no namespace (one
      path component, as in `reg.example/support`) counts only its own
      repository as the same namespace;
    - a variant anywhere else is pulled **anonymously**: no request for
      it (manifest, config, layers or index) carries your credentials. A
      private image there fails with the registry's authorization error,
      and the message says the variant is outside the support image's
      namespace and was fetched without credentials.

    A variant in a *different registry* is also refused if that registry
    is a local or private address: `localhost`, `*.localhost`, `*.local`,
    a loopback, link-local, private or unspecified IP literal, or any host
    contemper would contact over plain HTTP. Otherwise a published support
    image could make contemper send requests to services on the build
    host's network. Variants in the support image's own registry are not
    affected, so a support image and its variants may all live on a local
    registry.

    Labels are content the image controls. Without credentials they
    cannot make contemper pull a private image you have access to into
    the disk. Local `oci-archive:`, `oci:`, `docker-archive:` and
    `docker-daemon:` references are refused, so labels can never point
    contemper at files on the build host, and a repository path with `.`
    or `..` segments is refused. On some registries (Amazon ECR,
    single-tenant registries) the first path component is not an
    ownership boundary, so the same-namespace rule gives less protection
    there.
- **Support image from a local archive or layout:** `.image` may be a
  registry reference in any registry, or a local reference, pulled with
  your credentials. You supplied that file directly, so its labels are
  trusted like your own command line. Relative local paths resolve
  against the current working directory, as they do on the command line.

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
   labels are never read, so a variant image cannot declare further
   branches of its own.
5. The final filesystem is the source image's layers, then the support
   image's own layers, then each winning variant's layers, **in branches
   sorted by name**. This order is fixed regardless of label order, so it
   doesn't depend on map iteration or how the labels happened to be
   written.

There is no priority ordering and no expression language: AND is
"require several paths in one variant", OR is "declare two variants in
the same branch". If two variants can both match at once, their
predicates are wrong for that axis.

## Worked example

A support image with two independent branches: `init-system`, choosing
the service definition to install for whichever init system is present,
and `first-boot`, adding configuration only when a day-1 provisioning
tool is installed.

```dockerfile
LABEL io.contemper.branch.init-system.openrc.requires.files="/sbin/openrc-init" \
      io.contemper.branch.init-system.openrc.image="ghcr.io/example/support-openrc:v5" \
      io.contemper.branch.init-system.systemd.requires.files="/usr/lib/systemd/systemd" \
      io.contemper.branch.init-system.systemd.image="ghcr.io/example/support-systemd:v5" \
      io.contemper.branch.first-boot.cloud-init.requires.files="/usr/bin/cloud-init" \
      io.contemper.branch.first-boot.cloud-init.image="ghcr.io/example/support-cloud-init:v5" \
      io.contemper.branch.first-boot.default="none"
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
