# Support image resolution

Target customization lives in small overlay container images rather than
in contemper's own code.

**The base case is a plain root filesystem.** A support image can be
nothing but a filesystem: its layers merge into the authored image and
that's the entire mechanism. Everything below is additive. An image that
needs no conditions and declares no requirements needs no labels.

**One entry point per target.** contemper's configuration maps a target
to a single support image reference, its default (see [Default support
images](../guide/targets.md#default-support-images)). That image
declares its own variants in labels on its own image config, rather
than contemper keeping a list of every candidate. Adding a variant means
republishing one image, not cutting a contemper release. `--support`
replaces a target's default rather than stacking with it, so there is
still exactly one entry point per build.

**Labels declare two separate things.** *Requirements* state what
the authored image must provide, and can be unconditional: a target
whose agent needs a first-boot configuration mechanism declares that
flatly. *Variants* declare alternative versions of the support image plus
the conditions selecting each. A support image may use either, both, or
neither.

**Declarations are image config labels.** A support image is described
by an ordinary Containerfile and built with the same tooling as the VM
image itself (podman, buildah, docker buildx). A Containerfile can set
config labels with `LABEL`, but manifest annotations need engine-specific
build flags, so labels are the engine-neutral place for the declarations.

**A support image is extended with `FROM`.** Because the declarations are
labels, an image built `FROM` a published support image inherits them,
and a provider can add files to it, or override one variant's `.image`
label, without republishing the variants or touching contemper.

**Variants outside the support image's namespace are pulled
anonymously.** The labels are content the image controls. If they could
name any image and have it pulled with the user's credentials, a
third-party support image could make contemper pull a private image the
user has access to into the disk. A variant in the same registry and
namespace as the support image keeps the user's credentials, so a private
support image can have private variants. Any other variant is allowed, to
let a derived support image keep the published variants, but every
request for it is made without credentials: a private image there simply
fails. A variant in a different registry that is a local or private
address (localhost, loopback, link-local, private ranges, hosts contacted
over plain HTTP) is refused outright, since otherwise a published support
image could make contemper send requests to services on the build host's
network; variants in the support image's own registry are unaffected.

**Branch names are encoded in label keys**, for example
`io.contemper.branch.init-system.openrc.requires.files`. The alternative,
branch as a label *value*, forces parallel arrays that nothing
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
own support image is examined for labels. An image pulled in
because it won a branch is merged as-is; its labels are never read.
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
