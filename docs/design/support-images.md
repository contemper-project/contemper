# Support image resolution

Target customization lives in small overlay container images rather than
in contemper's own code.

!!! note "Implemented"
    Plain root-filesystem support images, `requires.files`, and the
    branch/variant mechanism described below are all implemented. See
    [Support image annotations](../reference/support-image-annotations.md)
    for the schema reference.

**The base case is a plain root filesystem.** A support image can be
nothing but a filesystem: its layers merge into the authored image and
that's the entire mechanism. Everything below is additive. An image that
needs no conditions and declares no requirements needs no annotations.

**One entry point per target.** contemper's configuration maps a target
to a single support image reference. That image declares its own
variants in annotations on its own manifest, rather than contemper
keeping a list of every candidate. Adding a variant means republishing
one image, not cutting a contemper release.

**Annotations declare two separate things.** *Requirements* state what
the authored image must provide, and can be unconditional: a target
whose agent needs a first-boot configuration mechanism declares that
flatly. *Variants* declare alternative versions of the support image plus
the conditions selecting each. A support image may use either, both, or
neither.

**Branch names are encoded in annotation keys**, for example
`io.contemper.branch.init-system.openrc.requires.files`. The alternative,
branch as an annotation *value*, forces parallel arrays that nothing
keeps index-aligned once a support image belongs to more than one
branch.

**Branches are orthogonal axes, not a tree.** The entry point's own root
filesystem merges unconditionally and carries what every variant shares
(the agent binary, common boot configuration), with winning variant
layers stacking above it, so a variant contributes only its differences.
Two axes are real today: `init-system`, selecting the agent's service
definition, and `first-boot`, selecting configuration for whichever
day-1 provisioning mechanism is present. Any init system can pair with
any first-boot mechanism, which is exactly what makes them axes. A tree
would need one leaf image per combination, 2×2 now and growing
multiplicatively; orthogonal axes need one image per value, 2+2.

**Architecture is deliberately not an axis.** Support images resolve
through standard OCI image indexes exactly as source images do, so a
variant that differs only by architecture is a multi-arch index rather
than a branch.

**Support images drop in files; they never install.** A support image
may contribute config files and static binaries, and nothing else. It
cannot run a package manager, because contemper never executes image
content. That constraint is why the requirements mechanism exists: an
image that needs cloud-init present has no way to install it, so it
declares the requirement. Requirements state what must already be
present; variants adapt to *which* of several possible things is present.

**Static linking keeps the axis count down.** Contributed binaries carry
no libc or distro dependency, so distribution never becomes an axis of
its own, and contributed config files target locations the init system
or tool standardizes (`/etc/init.d`, `/etc/systemd/system`,
`/etc/cloud/cloud.cfg.d`) rather than distro-specific paths.

**Optional axes need declared defaults.** Since zero matches aborts the
build, a no-op default variant is how an axis expresses that it is
optional.

**Resolution is exactly one level deep, deliberately.** Only the target's
own support image is examined for annotations. An image pulled in
because it won a branch is merged as-is; its annotations are never read.
Transitive resolution would mean handling cycles, mutual references,
depth limits, and diagnostics for chains that fail several levels down.
A support image that seems to need a second level is usually better
modeled as another branch on the entry point.

**Predicates are file-existence checks only.** No expression language,
no interpreter. AND is "list several required paths in one variant"; OR
is "define two variants in the same branch".

**Resolution is deterministic and fails loudly.** Exactly one variant per
branch must match, or its declared default applies. Zero matches or
several both abort the build, naming the branch and the paths checked.
No priority ordering and no tie-breaking: if two variants can both match,
their predicates are wrong.

**Manifest reads come before layer pulls.** Candidate evaluation only
needs manifests, so variants that lose are never downloaded. The
merged-view path index predicates are checked against is built during
the layer walk contemper does anyway.

!!! tip "Pick load-bearing markers"
    Predicates should target the actual init binary, not incidental
    markers such as a systemd library directory that some minimal images
    carry without systemd being PID 1. A false positive silently installs
    the wrong service integration and is only discovered at boot.
