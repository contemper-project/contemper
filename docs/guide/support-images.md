# Support images

A support image carries what a target needs that your image shouldn't
have to know about: an agent binary, service definitions, boot
configuration. It is an ordinary OCI image.

**In its simplest form it is just a root filesystem.** Its layers merge
on top of your image, with no labels and no conditions. Most
support images need no more than this.

```console
$ contemper convert --target qemu --support ghcr.io/example/extras:v1 \
    oci-archive:my-appliance.tar
```

**Support images drop in files; they never install anything.** A support
image contributes config files and static binaries, and nothing else.
Its layers are merged, never executed, so it cannot install something it
depends on. If a target needs cloud-init, your image has to already have
cloud-init; the support image supplies the configuration, not the
software.

## Requirements

A support image can declare paths your image must provide, as an
label on its image config:

```dockerfile
LABEL io.contemper.requires.files="/usr/bin/cloud-init,/sbin/openrc-init"
```

Every listed path must exist in the merged filesystem, or the conversion
fails naming the missing ones. This is how a support image asks for
something it cannot install itself.

## Variants

A support image can declare **variants** of itself, each selected by
file-existence conditions, grouped into independent **branches**:

- `init-system` picks the agent's service definition, an OpenRC init
  script or a systemd unit, by which init binary is actually present.
- `first-boot` picks configuration for whichever day-1 provisioning tool
  is installed.

The branches resolve independently, so any init system pairs with any
first-boot mechanism without a variant per combination. Architecture is
not a branch: support images resolve per architecture through ordinary
multi-arch image indexes.

Selection is deterministic: exactly one match per branch, or a declared
default. Zero matches or several both fail the build, naming the branch
and the paths checked. Candidates are evaluated by manifest first, so
losing variants are never downloaded.

Resolution is one level deep. Only the target's own support image is
examined for labels; an image pulled in because it won a branch is
merged as-is. That rules out cycles and unbounded resolution chains by
construction.

A support image declares a branch with labels on its own image config,
naming the branch and variant in the key. It is built from an ordinary
Containerfile, with the same tooling as any other image (`podman`,
`buildah`, `docker buildx`):

```dockerfile
FROM scratch

# Where the variants live. Defaults are tags; pin digests at build time.
ARG OPENRC_IMAGE=ghcr.io/example/support-openrc:v5
ARG SYSTEMD_IMAGE=ghcr.io/example/support-systemd:v5

# The support image's own files, merged for every build.
COPY --chmod=0755 agent /usr/local/bin/agent

LABEL io.contemper.branch.init-system.openrc.requires.files="/sbin/openrc-init" \
      io.contemper.branch.init-system.openrc.image="${OPENRC_IMAGE}" \
      io.contemper.branch.init-system.systemd.requires.files="/usr/lib/systemd/systemd" \
      io.contemper.branch.init-system.systemd.image="${SYSTEMD_IMAGE}"
```

Each variant is itself a Containerfile that is just files:

```dockerfile
FROM scratch
COPY --chmod=0644 agent.openrc /etc/init.d/agent
```

Build and push the variants first, as multi-arch images, then build the
support image with each variant pinned by the digest of its pushed index,
so that a given support image always names exactly the variant images it
was built against. Each variant, and the support image itself, resolves
per architecture the same way as any other image:

```console
$ for v in openrc systemd; do
    podman build --platform linux/amd64,linux/arm64 \
        --manifest ghcr.io/example/support-$v:v5 -f Containerfile.$v .
    podman manifest push --digestfile $v.digest \
        ghcr.io/example/support-$v:v5 docker://ghcr.io/example/support-$v:v5
  done
$ podman build --platform linux/amd64,linux/arm64 \
    --manifest ghcr.io/example/support:v5 \
    --build-arg OPENRC_IMAGE=ghcr.io/example/support-openrc@$(cat openrc.digest) \
    --build-arg SYSTEMD_IMAGE=ghcr.io/example/support-systemd@$(cat systemd.digest) .
$ podman manifest push ghcr.io/example/support:v5 docker://ghcr.io/example/support:v5
```

Given an image with `/sbin/openrc-init` present, `convert` fetches and
merges only `support-openrc`, reporting which variant it picked and why:

```console
🧩  support image · ghcr.io/example/support:v5
    ✔ rootfs                                      3 layers · 640 KiB download
    ✔ branch init-system → openrc                  matched /sbin/openrc-init
      └ ghcr.io/example/support-openrc:v5 linux/arm64 · 210 KiB download
```

The download size comes straight from the manifest's layer descriptors,
so it's known - and shown - before anything is pulled; a losing variant's
size is never shown, because it's never fetched. Once the layers are
actually merged, `convert` also reports what each piece added to the
root filesystem:

```console
🧬  merging 12 + 4 layers
    ✔ support image                                +9 files · 580 KiB
    ✔ variant init-system=openrc                    +3 files · 60 KiB
```

That's a count of the filesystem entries and regular-file bytes each
piece writes on its own terms - reading only that image's own layers,
never the (often much larger) base image or anything else it's merged
onto - plus a "N removed" note when it carries a whiteout or opaque
directory marker. It answers "what does this image write", not "how
much did the merged filesystem grow by"; a path this image writes that
also happened to already exist is still counted; a removal is counted
whether or not there was ever anything at that path to remove. Both
numbers are purely informational and aren't recorded in the bundle.

The full label schema, the resolution algorithm, and a worked example
with two branches are in [Support image
labels](../reference/support-image-labels.md); why the mechanism looks
the way it does is in [Design: support image
resolution](../design/support-images.md).

## Where variants may live

A support image pulled from a registry may name variants in any registry
or namespace. A variant in the same registry and namespace as the
support image is pulled with your registry credentials, so a private
support image can have private variants. Anywhere else, the variant is
pulled anonymously, without your credentials, and a private image there
fails with the registry's authorization error. The image's labels are
content the image controls; this keeps them from making contemper pull a
private image you have access to. The details are in [Support image
labels](../reference/support-image-labels.md#keys).

## Extending a published support image

Because the declarations are image labels, a support image can be
extended with an ordinary `FROM`. The result inherits the labels, so the
published variants still apply, and the files you add merge in with the
support image's own:

```dockerfile
FROM ghcr.io/contemper-project/incus-support:v1
COPY --chmod=0644 my-agent.conf /etc/my-agent/agent.conf
```

Build it, push it, and pass it with `--support`. The inherited variants
live in `contemper-project`, outside your namespace, so they are pulled
anonymously; that works because the published ones are public. In
general, a variant outside the derived image's own registry and
namespace is fetched without credentials, so it must be public.

To change what one variant contributes, override its `.image` label with
an image of your own, built `FROM` that variant so it keeps everything
the original provides:

```dockerfile
FROM ghcr.io/contemper-project/incus-support:v1
LABEL io.contemper.branch.init-system.openrc.image="registry.example/team/my-openrc:v1"
```

```dockerfile
# registry.example/team/my-openrc:v1
FROM ghcr.io/contemper-project/incus-support-init-system-openrc:v1
COPY --chmod=0755 my-openrc-hook /etc/local.d/my-hook.start
```

## Naming

contemper's own support images (the ones a target uses by default; see
[Targets](targets.md#default-support-images)) are developed in the
[support-images repository](https://github.com/contemper-project/support-images)
and released independently of contemper. The `incus` target uses
`ghcr.io/contemper-project/incus-support:v1` unless `--support` names
another. They follow one convention:

- The entry point for `<name>`'s target lives at
  `ghcr.io/contemper-project/<name>-support`, for example
  `ghcr.io/contemper-project/incus-support`.
- A branch's variant image lives at
  `ghcr.io/contemper-project/<name>-support-<branch>-<variant>`, for
  example `ghcr.io/contemper-project/incus-support-init-system-openrc`
  for the `init-system` branch's `openrc` variant.

This is a naming convention for contemper's own images, not a
requirement the resolution mechanism enforces: a support image's
`io.contemper.branch.*.image` labels can point anywhere. Following
it just keeps a target's own images discoverable and consistently
named.
