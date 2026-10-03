# Design: framing

This section records *why* contemper is the way it is, including paths
considered and rejected, so those discussions don't get reopened from
scratch.

Containers arguably became what they are largely because Docker
introduced the `Dockerfile`, a construct that made image authoring easy
and understandable in a way its predecessors weren't. That's the bet
behind contemper: the same authoring ergonomics should work just as well
for VM images, which are still mostly built with heavier, more bespoke
tooling.

Authoring a VM image today means a separate toolchain from authoring a
container image (different base image conventions, different build
tooling, different registry habits) even though the actual content, a
filesystem plus metadata about how to boot it, is conceptually the same
problem.

## Authoring, not conversion

The distinction the whole design rests on: contemper is a VM image
**authoring** tool that happens to use container build tooling as its
interface, not a **conversion** tool for arbitrary container images.

Mechanically the two look identical: both take an OCI image and produce
a bootable disk. But they set entirely different expectations. A
conversion tool implies the input is any container image, and the tool's
job is to make it work regardless of what's inside. An authoring tool
implies the image was built for this purpose, and the tool's job is to
package what the author already provided.

This is why support images *supplement* an authored image rather than
trying to detect and repair arbitrary containers, and why missing
fundamentals (kernel, initrd, init) are a hard error rather than
something contemper tries to fix.

The goal is that building a custom VM image feels no different from
building a custom container image: the same build files, the same tools,
the same registries, without needing to know a target's integration
requirements up front.

## On having to install a kernel and an init system

The obvious objection is that requiring the author to supply a kernel,
an initrd and an init system is a burden contemper imposes. It isn't.
Installing a kernel and an init system is what installing an operating
system *is*. Every tool that builds an OS from packages does exactly
this, and contemper does not add a step. It changes which tool drives
the step, not how much work the step is. mkosi runs a distro installer
to do it; distrobuilder runs a package manager; contemper is
`RUN apk add linux-virt openrc` in a build file.

The contrast that makes this feel sharper than it is comes from
workflows that start from an existing image: Packer building from a
cloud image or AMI, and virt-builder, both starting from an image that
already had a kernel installed by whoever built it. Their users never
perform this step. (Packer can also drive a full install from an ISO,
the same step contemper's authors do with their own package manager;
the contrast is with the cloud-image workflow, not with Packer as a
tool.) bootc's users don't perform this step either, because its base
images have had it done for them. contemper sits with mkosi and
distrobuilder: you install the kernel, because you are building an OS
rather than customizing one that already exists.

Stock container bases lack these components not because they are
deficient. `alpine:latest` carries no kernel because the host's kernel
is already there, and no OpenRC because the container runtime occupies
PID 1. Those omissions are correct engineering for a container image.
Building for a VM is building for a different execution model, in which
those roles are suddenly yours to fill.

Two qualifications:

- **Initrd generation needs a flag it wouldn't need on a live system.**
  dracut and mkinitcpio default to host-only mode, probing the hardware
  of the machine they run on, which during a container build is the
  *build* host, not the VM. They must be forced into generic mode
  (`--no-hostonly` or the equivalent). Alpine's mkinitfs may need
  explicit invocation rather than relying on a kernel package's
  post-install hook firing correctly in a build context.
- **The kernel command line is a genuine exception.** On a conventional
  install nobody writes a cmdline; `grub-install` and its equivalents
  generate one. contemper has no bootloader installer (images may
  [bring their own bootloader](bootloader-images.md)), and a UKI seals
  the cmdline in at build time, so this really is a new authoring input.
  It is not contemper-specific (anyone using `systemd-ukify` faces the
  same thing), but it is not "the same as installing an OS" either.

"Blessed" base images that already satisfy these requirements, as
bootc's do, would remove the main sharp edge. If they exist, they are
this install done in advance, exactly as a cloud image is for Packer: a
convenience, never a requirement. See [open questions](open-questions.md).
