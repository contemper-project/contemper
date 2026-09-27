# Volumes and providers

## Volumes

Persistent volumes are identified by the orchestrator when they are
attached, for example through a device `serial=` field, not by scanning
filesystem labels across a fleet. First attachment formats and labels an
empty volume; later boots recognize the label. Because identity is owned
by the orchestrator rather than by a fleet-wide lookup, label collisions
between unrelated volumes are harmless.

The exact identifier mechanism is provider-specific. contemper assumes
*some* stable identifier exists but does not prescribe one.

### Planned design

Volumes are the next milestone. The decisions so far:

- **Block devices everywhere.** Every target attaches volumes as blank
  block devices, including Incus, which also offers filesystem volumes.
  Block devices are what every provider can offer, so one mechanism
  covers all of them.
- **The guest formats on first boot.** Only a local hypervisor lets
  contemper create disks itself; real providers hand the VM a blank
  device. So formatting always happens in the guest, never on the host.
- **Declaration.** `VOLUME` in the image, plus labels
  `io.contemper.volume.<path>.size`, an optional
  `io.contemper.volume.<path>.name`, and `io.contemper.root.size` for
  the root disk. Command-line flags override them. A volume without a
  size is recorded without one, and deployment fails until a label or a
  flag provides it; contemper never guesses a size.
- **Names.** A volume is identified by its path. Its name, at most 16
  characters (the ext4 label limit), is derived from the path unless the
  optional name label overrides it, which keeps a volume attached when
  its path changes between image versions. Derivation: the
  leading `/` dropped and the remaining `/` turned into `-`, and, when
  that is too long, shortened with a hash of the path appended. Two
  volumes with the same name fail the conversion. The name is the
  filesystem label and, where the provider lets contemper choose, the
  disk serial.
- **fstab.** contemper appends one line per volume,
  `LABEL=<name> <path> ext4 defaults,nofail 0 2`, the same on every
  target. Images opt out with `io.contemper.fstab="false"`, builds with
  `convert --no-fstab`.
- **Guest metadata in `/etc/contemper/`.** contemper writes facts known
  at conversion time, never deployment-time data, which stays with the
  provider's own metadata channel. `build` is written for every image:
  `key=value` lines with the contemper version, target, architecture,
  source reference and digest, and support image digests, without a
  timestamp so the root filesystem stays reproducible. A local archive
  source is recorded by file name and digest only, so no paths from the
  build machine end up in the guest. `volumes` is
  written whenever volumes are declared, even with the fstab opt-out:
  one volume per line as `name serial-pattern fs mountpoint`, with the
  mount path last so it may contain spaces.
- **A shell-script helper.** First-boot formatting is a POSIX `sh`
  script, identical in every image. It ships as a published support
  image built from this repository, with one variant per init system
  (OpenRC and systemd), and is merged only when an image declares
  volumes. A flag replaces it with another image or leaves it out. An
  image with volumes but neither init system fails the conversion
  rather than silently getting unformatted volumes. The helper reads
  `/etc/contemper/volumes` line by line and never sources it. A script
  rather than a binary keeps it architecture-independent and readable
  in the guest; the image must provide `mkfs.ext4`.
- **Reuse first, format only blank disks.** On every boot the helper
  checks each volume. An ext4 filesystem carrying the expected label is
  reused as-is and mounted, which is what lets a new image version run
  on the data of the previous one. A disk whose first and last MiB are
  all zeros is blank and gets formatted. Anything else is left alone and
  logged as a mismatch, so the failure mode is an unmounted volume,
  never lost data.
- **Local instances.** `deploy --to local-qemu` keeps volume disks per
  instance, named after the image repository without its tag, so
  deploying a new tag of the same image reuses the volumes while the
  root disk starts fresh.
- **Manifest.** Volumes in `contemper.json` become objects (name, path,
  size, filesystem), with a new format version. The same change nests
  the resolved support-image variants inside the `support` object.

## Providers

Two providers come first:

- **qemu** is the reference case: it proves the disk boots with nothing
  but a hypervisor, and doubles as the debugging baseline. Implemented
  as `deploy --to local-qemu`.
- **Incus** is the real integration case, exercising agent injection,
  configuration and volumes. **Planned.**

The Incus provider must assume a **remote** Incus server. Local paths are
not a safe assumption anywhere in the deploy path: the disk must be
transferable, and after import everything refers to the image by
fingerprint or alias rather than by file path.

## Driving provider tools

Providers are implemented by running their own CLI tools as
subprocesses, which keeps provider-specific knowledge in the tool that
owns it. The conventions:

- Parse only structured output (`--format json`, `--output=json`), never
  human-readable text.
- Run tools with argument arrays, never shell strings.
- Check the tool is present, and its version, before doing any work.
- Pass the tool's error output through verbatim on failure.
- Decide partial-failure and rollback behavior explicitly, since
  subprocesses give no transactions.
