# contemper

**Build bootable VM images from container images.**

contemper is a Go CLI that takes an OCI image you already built, merges
in what a given virtualization target needs, and writes out a bootable
disk. Authoring a VM image becomes an ordinary container build.

```console
$ <tool> build -t registry.example.com/my-appliance:dev .
$ <tool> push registry.example.com/my-appliance:dev
$ contemper convert --target qemu registry.example.com/my-appliance:dev
$ contemper deploy --to local-qemu my-appliance-dev.aarch64/
```

`<tool>` is `podman` or `docker`; the two commands above are identical
either way. With Docker, `contemper build` runs the build and the
convert step together and skips the registry; see
[Getting started](getting-started.md) for that walkthrough.

!!! info "Status"
    contemper is pre-1.0, and the CLI and bundle format may still change.
    `build` (with Docker), `convert` and `deploy --to local-qemu` work end
    to end: CI boots a converted image on every change. Features that are
    designed but not built yet are marked **Planned** throughout these
    docs. See the [roadmap](design/roadmap.md).

## What contemper is

An **authoring** tool, not a conversion tool. It expects an image built
deliberately for VM output, not an arbitrary application container
that it tries to make bootable. The container build is how you author
the filesystem; contemper handles the target-specific packaging around
it.

It never parses your build file. contemper consumes already-built OCI
images, so the build file's name, syntax, and the tool that ran it are
entirely yours to choose.

It executes nothing from your images. contemper copies files and merges
layers, and that is all it does to your image's contents. Every
decision comes from reading manifests and inspecting files read-only,
and nothing from your image is ever run on the machine doing the
conversion. It does run a few known host tools (`mkfs.ext4`, `qemu-img`),
which is a different thing. Your kernel and workload do of course run:
in the VM, once the disk boots.

## Where to go next

- [Getting started](getting-started.md): build the example image,
  convert it, and boot it.
- [Authoring an image](guide/authoring.md): what your image has to
  provide, and what contemper reads from it.
- [Distributions](guide/distributions.md): what each distribution family
  needs, and which ones are covered.
- [How conversion works](guide/how-it-works.md): the pipeline, step by
  step.
- [Comparison](comparison.md): how contemper relates to bootc, Packer,
  mkosi and others.
- [Design](design/index.md): the reasoning behind the choices, including
  the approaches that were rejected.
