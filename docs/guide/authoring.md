# Authoring an image

## What you provide

A VM needs a kernel, an initrd, a kernel command line, and an init
system. **Supplying these is the author's job.** contemper does not
install a kernel, generate an initrd, choose kernel parameters, or set
up an init system.

This is not an extra step contemper invents. Installing a kernel and an
init system is what installing an operating system *is*; contemper
changes which tool drives that step, not how much work it is. In
practice it's a line in your build file, `RUN apk add linux-virt openrc`
or your distro's equivalent: the same package from the same package
manager you'd use anywhere else.

Stock container bases don't carry these components, and that's correct
of them rather than a deficiency. `alpine:latest` has no kernel because
the host's kernel is already there, and no OpenRC because the container
runtime is PID 1. In a container they'd be inert weight. A VM is a
different execution model, and those roles become yours to fill.

Three clarifications:

- contemper has no opinion on *which* init system, or (for its own
  purposes) whether one exists at all. The requirement comes from
  reality, since a Linux VM needs one to boot, not from contemper.
- contemper has no opinion on how a booted system gets its first-boot
  configuration. cloud-init is one common answer, but that's a property
  of your image and your provider. A target's support image may
  *require* such a mechanism, in which case it declares that explicitly.
- Generate your initrd in *generic* mode, not host-only mode. dracut and
  mkinitcpio default to probing the hardware of the machine they run on,
  which during a container build is your build host rather than the VM
  that will boot the image. Pass `--no-hostonly` or the equivalent.

## The fixed-path contract

Kernel, initrd and an optional command line go at fixed paths, the same
for every target:

| Path | Meaning |
| --- | --- |
| `/boot/contemper/vmlinuz` | the kernel |
| `/boot/contemper/initrd` | the initrd (generic, not host-only) |
| `/boot/contemper/cmdline` | optional; extra kernel command line parameters, e.g. `console=` |
| `/sbin/init` | must exist |
| `/etc/os-release` | optional; if present, embedded in the boot image |

Symlinks are fine: `/boot/contemper/vmlinuz → ../vmlinuz-virt` is how
the example does it. They are resolved inside the image's filesystem,
never on the host, with a hop limit and clamping at `/`.

The cmdline file is optional, and so is anything in it: contemper
always writes `root=LABEL=contemper-root` at the front of the kernel
command line, since it creates that partition itself and labels it
`contemper-root`. There's no bootloader installer to generate the rest
for you, so leave the file out, or leave it blank, unless you have
parameters to add beyond `root=` — extra options such as `console=`,
for example:

```text
rootfstype=ext4 rw console=ttyAMA0
```

Use `console=ttyS0` on x86-64. Your part comes after contemper's, so a
`root=` of your own takes precedence. Resolving `LABEL=` is done by the
initrd; every common initrd generator (dracut, initramfs-tools,
mkinitcpio, Alpine's mkinitfs) supports it. Refer to the same label in
`/etc/fstab`:

```text
LABEL=contemper-root / ext4 rw,relatime 0 1
```

## Marking the image ready

Mark an image as intended for contemper in the build file:

```dockerfile
LABEL io.contemper.ready="true"
```

It asserts intent only. It does not imply the requirements above are
met; those are checked later, against the merged filesystem, and an
unprepared image fails there regardless.

What the label buys is *where* that failure happens. It lives in the
image config, so it can be read and rejected before a single layer is
transferred. Rejecting an image for a few kilobytes rather than after
pulling gigabytes is the difference between a cheap error and an
expensive one.

## What contemper reads from your image

contemper never parses your build file; it reads the built image. Most
build instructions leave no trace there at all: `RUN`, `COPY`, `ADD`,
`ARG` and `FROM` produce the filesystem, which *is* the input. A handful
of instructions record fields in the image config, and contemper reads
only some of them.

| Instruction | contemper |
| --- | --- |
| `LABEL` | **read**: readiness marker and contemper configuration |
| `VOLUME` | **read**: named, sized and formatted on first boot - see [Volumes](volumes.md) |
| `EXPOSE`, `HEALTHCHECK` | **read**: recorded as deployment hints; not used for the disk |
| `ENTRYPOINT`, `CMD` | ignored |
| `USER`, `WORKDIR` | ignored |
| `ENV` | ignored |
| `STOPSIGNAL` | ignored; shutdown is your init system's job |
| `ONBUILD` | ignored; its triggers fire on a later build, never here |

Ignored means exactly that: contemper reads the field, uses it for
nothing, and does not complain. It never fails a build over a config
field it doesn't consume, because most of these are inherited from your
base image rather than written by you. `FROM alpine` sets a `CMD` you
never typed.

**Your `CMD` does not run.** In a container the runtime executes your
entrypoint as PID 1. In a VM, PID 1 is your init system. To start your
workload, ship a service for it in your image (an OpenRC service, a
systemd unit, whatever your init system uses), exactly as you would when
installing software on any other machine.

The same applies to `ENV`: there is no container runtime to inject it
into your process environment, so set what your service needs the way
your init system expects. A target's support image may bridge some of
these; that's up to the support image, not contemper.

## A complete example

The repository's `examples/alpine/Containerfile` is a minimal image that
satisfies all of the above: a kernel, a generic initrd built with
`mkinitfs`, a command line, OpenRC with a serial login, and a boot marker
printed to the console. [Getting started](../getting-started.md) walks
through building and booting it.
