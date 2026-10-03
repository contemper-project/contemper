# Open questions

**Deployment metadata, the largest open item.** How volumes are declared,
formatted and mounted is settled (see
[volumes and providers](volumes-and-providers.md#planned-design)). What
remains is the deployment side: which metadata a provider acts on, and
what does the acting. Leaving that to cloud providers can't bootstrap,
since it asks a provider to support a format with no users; a
`contemper deploy` with provider glue can, at the cost of scope. These
aren't really alternatives: the metadata format has to be designed either
way, and only its first consumer differs. The
[bundle manifest](../reference/bundle.md) is that format's starting
point, anchored on primitives that already exist (`VOLUME`, `EXPOSE`,
`HEALTHCHECK`), with new declarations only for what those can't express.

Whether OpenTofu is involved is undecided. Embedding it has a hard
blocker (its core lives under `internal/`, so it can't be imported as a
library without forking), leaving bundling a large binary or discovering
`tofu` on `PATH`. Also open: who owns state.

**Multi-architecture sources.** If the source is a multi-arch index,
contemper could build every architecture present, require an explicit
`--arch`, or default to the host's. It currently defaults to the host's;
this is a UX question more than a technical one.

**Provenance.** This is about attaching provenance to contemper's
*output*: the bundles `convert` produces for an image you author, beyond
what the [bundle manifest](../reference/bundle.md) already records.
contemper's own release artifacts and published support images now
carry signed GitHub build provenance (see [verifying
downloads](../getting-started.md#verifying-downloads) and the [volumes
guide](../guide/volumes.md)), but whether a build's output bundle gets
the same treatment is still open. An OCI referrer would be the natural
shape there too, since cosign and oras can inspect it without
contemper-specific tooling.

**Blessed base images.** Curated base images that already satisfy the
kernel, initrd and init requirements would remove the main sharp edge in
authoring. They must be a convenience, never a requirement, since
accepting arbitrary bases is a deliberate difference from bootc. Also
undecided: contemper-maintained or merely contemper-documented, since
maintaining a family of base images is an ongoing commitment unlike
maintaining a CLI.

**Incus boot order.** Whether Incus's OVMF build tries network boot
before the fallback path, which would show up as a boot delay rather than
a failure. Unverified.

**Images with their own bootloader.** The contract is recorded in
[bootloader images](bootloader-images.md). Still open there: how the guest
finds the ESP to mount it, and the exact GRUB recipe for Debian.
