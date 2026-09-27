# Source references

`convert` takes one source reference: the image to convert. `--support`
takes another, in the same forms.

| Form | Meaning |
| --- | --- |
| `ghcr.io/example/app:v1` | a registry reference, pulled with your registry credentials (the same keychain docker and podman use) |
| `oci-archive:<path>` | an OCI archive, as written by `podman save --format oci-archive` or `docker save` (Docker 25 and later) |
| `oci:<path>` | an OCI image layout directory |
| `docker-archive:<path>` | a docker archive, as written by `docker save` (Docker 24 and earlier, or with the classic image store) or `podman save --format docker-archive` |

There is no container-daemon source. Save the image to an archive and
point `convert` at that instead. Nothing needs to be pushed to a
registry to iterate locally.

`docker save` output works as `oci-archive:` regardless of which image
store Docker is using. With the classic image store, the archive's
`index.json` points straight at the image manifest. With the containerd
image store, `index.json` points at a second image index, which in turn
lists the per-platform manifests alongside attestation manifests;
`convert` follows that nesting and ignores the attestation manifests
when selecting a platform.

## Platform selection

`convert` builds for the host architecture unless `--arch amd64|arm64`
says otherwise. When a reference names a multi-platform index, the
manifest for that platform is selected before any layer is fetched,
following nested indexes and skipping attestation manifests along the
way; if there is no manifest for the requested platform, the conversion
fails at that point.

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

## Recorded references

The bundle manifest records each reference in the same form, with local
paths cleaned up (`a/../b.tar` becomes `b.tar`), and the digest of the
image that was actually used. Bundles from local archives and layouts
are marked as not reproducible, since a path can't be fetched again the
way a registry digest can.
