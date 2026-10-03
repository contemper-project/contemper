# Source references

`convert` takes one source reference: the image to convert. `--support`
takes another, in the same forms.

| Form | Meaning |
| --- | --- |
| `ghcr.io/example/app:v1` | a registry reference, pulled with your registry credentials (the same keychain docker and podman use) |
| `oci-archive:<path>` | an OCI archive, as written by `podman save --format oci-archive` or `docker save` (Docker 25 and later) |
| `oci:<path>` | an OCI image layout directory |
| `docker-archive:<path>` | a docker archive, as written by `docker save` (Docker 24 and earlier) or `podman save --format docker-archive` |
| `docker-daemon:<ref>` | an image already loaded into the local Docker daemon, e.g. `docker-daemon:my-app:dev` |

`docker-daemon:<ref>` needs `docker` on `PATH`. It runs `docker save`
into a temporary archive, reads that back with the same code the
`oci-archive:`/`docker-archive:` forms use, and removes it once the
image has been converted; nothing is kept on disk afterwards, and there
is no dependency on the Docker API socket. `contemper build` uses this
form to convert the image it just built; `contemper convert
docker-daemon:my-app:dev` is also how to convert an image built (or
pulled) by hand, without an explicit `docker save` step.

`docker save` output works as `oci-archive:` regardless of which image
store Docker is using. With the classic image store, the archive's
`index.json` points straight at the image manifest. With the containerd
image store, `index.json` points at a second image index, which in turn
lists the per-platform manifests alongside attestation manifests;
`convert` follows that nesting and ignores the attestation manifests
when selecting a platform.

## Platform selection

`convert` builds for the host architecture unless `--arch amd64|arm64`
says otherwise; `--arch` also takes a comma-separated list or `all` to
convert several architectures in one run (see
[multi-architecture](../guide/multi-arch.md#converting-several-architectures-at-once)).
When a reference names a multi-platform index, the
manifest for that platform is selected before any layer is fetched,
following nested indexes and skipping attestation manifests along the
way; if there is no manifest for the requested platform, the conversion
fails at that point. For a list or `all`, every requested platform is
checked against the index up front. A `docker-daemon:` source can't be
listed without a `docker save`, so it accepts one explicit architecture
only.

## Bundle naming

The bundle directory is named `<base>-<tag>.<arch>`. For an
`oci-archive:`/`oci:` source, `<base>` and `<tag>` come from the first
naming annotation found, in this order:

1. `io.containerd.image.name` - the full reference containerd-backed
   tools (including Docker with the containerd image store) name an
   image with, e.g. `docker.io/library/example:dev`.
2. `org.opencontainers.image.ref.name` - the reference other tools
   (`podman save`, `buildah push`) record here as `name:tag`. Docker's
   containerd image store sets this to a bare tag instead (since the
   full reference is already in `io.containerd.image.name`), and that
   bare tag is read as `<tag>` rather than mistaken for `<base>`.

If neither annotation is present - a `buildx --output type=oci` archive,
for example - `<base>` falls back to the archive or layout's own name
(`example.tar` gives `example`) and `<tag>` defaults to `latest`.

A `docker-archive:` source is named from its `RepoTags` entry instead,
falling back the same way when there is none.

A `docker-daemon:<ref>` source is always named after `<ref>` itself
(`docker-daemon:my-app:dev` gives `my-app-dev.<arch>`), regardless of
what naming annotations the `docker save` output carries.

## Recorded references

The bundle manifest records each reference in the same form, with local
paths cleaned up (`a/../b.tar` becomes `b.tar`; a `docker-daemon:<ref>`
reference is kept as given, since `<ref>` is an image reference, not a
path), and the digest of the image that was actually used. Bundles from
local archives, layouts and the local Docker daemon are marked as not
reproducible, since none of them can be fetched again the way a
registry digest can.
