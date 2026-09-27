# Volumes

A volume is a `VOLUME` in your image that contemper backs with a real,
persistent disk: sized, named, formatted on first boot, and mounted
through `/etc/fstab` like any other filesystem. Every target attaches a
volume as a blank block device — even Incus, which also offers
filesystem volumes — because a block device is what every provider can
offer, and the guest prepares it the same way regardless of which
provider handed it over.

## Declaring a volume

```dockerfile
VOLUME /data
LABEL io.contemper.volume./data.size="10GiB"
```

`VOLUME` is what makes a path a volume at all; the label gives it a
size. Without a size, `convert` still records the volume — unsized — but
`deploy` refuses to create it until one is supplied, either by adding the
label or by passing `--volume /data=10GiB` at deploy time (which also
overrides a label's size, if you want a different size for one
deployment). contemper never guesses a size.

Sizes accept the same units everywhere in contemper: `10GiB`, `512MiB`,
or a bare byte count.

## Names

**A volume's identity is its path.** contemper derives a name from it
automatically — the leading `/` dropped, the rest lowercased, `/` turned
into `-`, and anything still outside `[a-z0-9-]` dropped, shortened with
a hash suffix if that's still longer than 16 characters (the ext4 label
limit). `/data` becomes `data`; `/var/lib/my-app` becomes
`var-lib-my-app`.

The name is what ends up on disk — the ext4 label, and (where the target
lets contemper choose) the disk's serial — but the path is what
contemper and the volume helper reason about. Give a volume an explicit
name only when you need one:

```dockerfile
LABEL io.contemper.volume./data.name="appdata"
```

The usual reason is keeping the *same* volume attached across an image
change that moves the path (`/data` renamed to `/var/lib/app`, say): the
derived name would change and contemper would treat it as a different
volume, but an explicit name carries over unchanged. A shorter, friendlier
label is a fine reason too. Two volumes that resolve to the same name,
explicit or derived, fail the conversion, naming both paths.

## fstab

contemper appends one line per volume to `/etc/fstab`:

```text
LABEL=data /data ext4 defaults,nofail 0 2
```

`nofail` means a missing or not-yet-formatted volume doesn't block boot;
that only actually happens if you skip the volume helper (below) on an
image with no other way to prepare the disk. Opt out of the fstab lines
entirely — if your init system mounts volumes its own way — with the
image label `io.contemper.fstab="false"` or `convert --no-fstab`.

## How the volume helper works

Formatting a blank disk has to happen somewhere, and only a local
hypervisor lets contemper create disks itself — every real provider
hands the VM a blank block device. So it always happens in the guest, on
every boot, by one rule:

- an ext4 filesystem already carrying the expected label is **reused**,
  unmounted and untouched — this is what lets a new image version keep
  running on the data an earlier version left behind;
- a disk whose first and last MiB are all zero is **blank**, and gets
  formatted and labelled;
- anything else is **left alone** and logged as a mismatch, so the
  failure mode is always an unformatted, unmounted volume — never lost
  data.

contemper ships this rule as a first-boot helper, merged into your image
automatically. Concretely, it's a support image like any other (see
[Support images](support-images.md)), published at
`ghcr.io/contemper-project/volumes-support`, with the actual logic in one
POSIX shell script and two variants — one per init system — that each
carry only the small integration needed to run it at boot:

```text
io.contemper.branch.init-system.openrc.requires.files=/sbin/openrc
io.contemper.branch.init-system.openrc.image=ghcr.io/contemper-project/volumes-support-init-system-openrc@sha256:...
io.contemper.branch.init-system.systemd.requires.files=/usr/lib/systemd/systemd
io.contemper.branch.init-system.systemd.image=ghcr.io/contemper-project/volumes-support-init-system-systemd@sha256:...
```

The variant images are named by digest, not a floating tag, so a given
`volumes-support:v1` always names an exact, reproducible pair of variant
images.

**When it merges.** Only when your image declares at least one volume
(no volumes, no helper — a plain `qemu` build never gains this layer). If
you also pass `--support`, that image and its own resolved variants merge
first; the volume helper merges after, so a user's own support-image
customizations are never shadowed by it. `--volume-helper <ref>`
substitutes a different image (hack/e2e-volumes.sh points this at a
locally built one); `--no-volume-helper` skips it entirely, in which case
*you* are responsible for getting volumes formatted and mounted — the
fstab lines and `/etc/contemper/volumes` are still written regardless,
since they describe the image's declared volumes, not who acts on them.

**Which variant wins.** Exactly like any support image's branches: the
`openrc` variant wins if `/sbin/openrc` exists in your merged image
before any support layers are merged, `systemd` wins if
`/usr/lib/systemd/systemd` does, and neither existing fails the
conversion — naming the branch and pointing at `--no-volume-helper` — so
a build never silently produces unprepared volumes. This also means an
image needs `mkfs.ext4` (from e2fsprogs — the same package that provides
the boot-time `fsck.ext4` most images already need) and `dd`/`od`
(from busybox or coreutils) available for the helper to do its job;
`convert` checks and fails naming whatever's missing. Unlike `fsck.ext4`,
`e2label`/`tune2fs`/`dumpe2fs` live in a separate `e2fsprogs-extra`
package on Alpine, so the helper deliberately doesn't use them: it reads
the label it needs straight out of the ext2/3/4 superblock with `dd` and
`od` instead (see the script itself,
`support/volumes-support/base/usr/lib/contemper/format-volumes`, for how).
`/sbin/openrc` matches equally whether your image keeps `/sbin` as a
real directory or, as on a merged-`/usr` distro (Fedora, Arch, current
Debian/Ubuntu), a symlink to `/usr/sbin`: predicate resolution follows a
symlink anywhere in the path, not only at the very end.

**What lands in the guest.** The base image contributes the script
itself, at `/usr/lib/contemper/format-volumes`. The winning variant adds
just its integration: OpenRC gets `/etc/init.d/contemper-volumes`
(`depend() { before fsck localmount }`) and its enablement symlink
`/etc/runlevels/boot/contemper-volumes`; systemd gets `/etc/systemd/system/contemper-volumes.service`
(`Before=local-fs-pre.target`) and its enablement symlink under
`local-fs-pre.target.wants/` — pre-created in the image, since a support
image only ever drops in files and never runs `systemctl enable` or
anything else. Either way it runs before local filesystems are checked
and mounted, so the
label is right by the time `/etc/fstab`'s `LABEL=` lines are resolved.

The systemd unit adds `DefaultDependencies=no` and
`After=systemd-udev-trigger.service`: by the time that trigger's own oneshot
has run, udev is already active and every disk already attached — root and
volumes alike, since every target hands them all over upfront rather than
hot-plugging — has had its boot-time ("coldplug") event replayed for udev to
process, without the (deprecated) full `udevadm settle`. Device discovery
itself needs no udev at all — `/sys/block/*/serial` is a sysfs attribute the
kernel populates at probe time — but formatting a blank disk does rely on
udev relabelling it afterwards, and that happens without any extra
`udevadm trigger` call: the kernel raises its own "change" event when an
exclusively-opened block device (how `mkfs.ext4` opens its target) is
closed, the same mechanism `parted` and `fdisk` rely on, and the
`LABEL=<name>` mount unit systemd generates from fstab is bound to the
resulting udev-created device unit rather than attempted once and given up
on, so it simply waits for the label to appear.

`convert` also writes `/etc/contemper/volumes`, one line per declared
volume (`name serial-pattern fs mountpoint`), which the script reads with
`while read -r` — never sourced.

**Finding a disk.** The script matches each volume's serial pattern
against `/sys/block/*/serial` (for `local-qemu` and Incus alike, this is
just the volume's name — see `--target`'s serial handling); the matching
block device is what gets checked and, if needed, formatted.

**Logging, and never failing boot.** Every decision is logged — to the
console, and to syslog if one is already running — naming the volume,
the disk, and what happened and why. The script always exits 0: a
mismatch or a formatting failure is visible in the log, never a reason to
stop the machine from booting.

**What's recorded.** The merged helper and its winning variant show up
next to any `--support` image, in their own `volumeHelper` field in
`contemper.json` and their own `volume-helper.*` lines in
`/etc/contemper/build` — ref, digest, and the resolved variant, so a
disk's exact provenance (support image and volume helper alike) is
always readable from the bundle or the guest itself.

## Deploying locally

`deploy --to local-qemu` keeps one qcow2 file per volume in a
per-instance state directory,
`${XDG_STATE_HOME:-~/.local/state}/contemper/local-qemu/<instance>`,
created on first deploy and reused on every later one — the disk survives
even though the root disk itself resets on every boot (`snapshot=on`).
The instance defaults to the source image's repository name, without its
tag, so redeploying a new tag of the same image reuses the same volumes;
`--name` overrides it, for running more than one instance of the same
image side by side.

```console
$ contemper deploy --to local-qemu _out/my-appliance-v2.aarch64/
📁  instance my-appliance                         ~/.local/state/contemper/local-qemu/my-appliance
    ✔ /data → data                                created (10.0 GiB)
```

contemper never resizes an existing volume disk. If a later deploy asks
for a different size than the disk already on file, it fails rather than
silently growing or shrinking it — remove the file yourself (or deploy
without changing the size) to start over.
