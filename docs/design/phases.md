# Lifecycle phases and the bundle

Getting from a fresh build file to a running VM has four phases. Only one
of them is contemper's own work.

| # | Phase | In | Out |
| --- | --- | --- | --- |
| 1 | **Author** | a base image, your application | build file and context |
| 2 | **Build** | build file and context | an OCI image |
| 3 | **Convert** | image reference and target | a contemper bundle |
| 4a | **Publish** | bundle and provider | an image in the provider's catalog |
| 4b | **Deploy** | bundle, provider, instance config | a running VM |

!!! warning "Partly planned"
    Build (with Docker), convert and `deploy --to local-qemu` are
    implemented. The `publish` adapter and Incus publish and deploy are
    designed but not built.

**Publish and deploy are alternatives, not a sequence.** Both consume a
bundle and differ in what they produce, and so in who uses them. Publish
makes a bundle available at a provider and stops: the case where an
appliance is published once and other people launch it. Deploy produces
an instance, publishing first where the provider needs a registered image
to launch from. Where a provider has no catalog, like plain qemu, publish
does not apply at all.

"Push" was rejected as a phase name twice over. For the image-to-registry
step it is a property of Build: buildx fuses it, and what it yields is the
same image made reachable rather than a new kind of thing. For the
terminal phase it would collide with the Build adapter, which may itself
push to a registry. Publish carries no such collision.

## Adapters for the other phases

contemper may drive every phase, through thin adapters over existing
tools: "run the build elsewhere, or let contemper run buildx for you"
rather than "assemble the tooling yourself". This conflicts with no
principle, since host tools are allowed and driving provider CLIs as
subprocesses was already the approach for providers.

Adapters are convenience, and convenience expands without limit unless
something stops it. Four rules do:

- Convert is contemper's own code; every other phase is an optional
  adapter.
- Adapters discover tools on `PATH` rather than bundling them, and when
  one is missing they print the exact command they would have run.
- Every phase stays skippable, so an existing pipeline loses nothing.
- One backend ships per phase to start: buildx for build, incus for
  publish and deploy. The interface allows a second backend; the
  generality isn't built until a requirement appears.

This implies a project configuration file, since the phases can't each
take target, source, provider and tool preferences as flags. That is not
a build file format: contemper still never parses a build file.

## The bundle

Convert emits a [bundle](../reference/bundle.md) rather than a bare disk.
The disk alone can't carry what Deploy needs (the image digests it came
from, the volumes the image declared, the resolved target), and a bare
`.qcow2` would force that metadata to travel separately.

Its representation is a plain directory: the disk with a
`contemper.json` beside it. No format to specify, no library to write,
inspectable with `ls` and `cat`. Tar or OCI-artifact serializations are
obvious later additions should distribution need them.

The manifest settles two questions at once. It *is* the deployment
metadata format, and it is the provenance record, since the digests
linking a disk to its inputs have to be recorded for Deploy to work
anyway.
