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
  the root disk. Command-line flags override them.
- **Names.** Each volume has a name of at most 16 characters, the ext4
  label limit, either given explicitly or derived from the path. The name
  is the filesystem label and, where the provider lets contemper choose,
  the disk serial.
- **fstab.** contemper appends one line per volume,
  `LABEL=<name> <path> ext4 defaults,nofail 0 2`, the same on every
  target. Images opt out with `io.contemper.fstab="false"`, builds with
  `convert --no-fstab`.
- **Guest metadata in `/etc/contemper/`.** contemper writes facts known
  at conversion time, never deployment-time data, which stays with the
  provider's own metadata channel. `build` is written for every image:
  `key=value` lines with the contemper version, target, architecture,
  source reference and digest, and support image digests, without a
  timestamp so the root filesystem stays reproducible. `volumes` is
  written whenever volumes are declared, even with the fstab opt-out:
  one volume per line as `name serial-pattern fs mountpoint`, with the
  mount path last so it may contain spaces.
- **A shell-script helper.** First-boot formatting is a POSIX `sh`
  script, identical in every image, shipped in a common contemper
  support layer with one variant per init system, and merged only when
  an image declares volumes. It reads `/etc/contemper/volumes` line by
  line and never sources it. A script rather than a binary keeps it
  architecture-independent and readable in the guest; the image must
  provide `mkfs.ext4`.
- **Blank means zeros.** The helper formats a disk only if its first and
  last MiB are all zeros. Anything else is left alone and logged, so the
  failure mode is an unformatted volume, never lost data.
- **Manifest.** Volumes in `contemper.json` become objects (name, path,
  size, filesystem), with a new format version.

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
