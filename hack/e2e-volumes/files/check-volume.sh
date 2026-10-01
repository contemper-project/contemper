#!/bin/sh
# Shared logic behind hack/e2e-volumes.sh's boot check. Both init-system
# integrations run this same script: the OpenRC one
# (zzz-contemper-volume-test.start) execs it directly, and the systemd
# one (contemper-volume-test.service) points ExecStart at it, so the
# check itself is never duplicated between the two.
#
# Checks two volumes, declared by ../Containerfile:
#
#   - /data has no seed label (so it seeds normally) and the image bakes
#     in a seed file at /data/seed.txt. On the first boot after its disk
#     was blank, that file must appear on the mounted volume with the
#     image's own content, owner and mode, and the volume's root
#     directory must carry /data's own owner and mode. The check then
#     appends a second line to seed.txt, so a later boot - where the same
#     disk is REUSED, never reformatted - can confirm that guest-made
#     change survived instead of being silently re-seeded over.
#   - /data-noseed carries io.contemper.volume./data-noseed.seed="false"
#     and bakes in its own seed file the same way, which must *never*
#     appear on the mounted volume, proving the opt-out works.
#
# Prints whether each volume is mounted, and on which device carrying
# which label, then the fresh-vs-persisted markers
# hack/e2e-volumes.sh expects for /data, plus the seeding markers it
# checks for both volumes. /data-noseed is checked first, since /data's
# fresh/persisted marker ends the boot.
#
# POSIX sh, no awk: the mount lookup below uses a plain `read` loop
# rather than `awk '$2 == "/data"'`, since awk isn't guaranteed present
# on an image built --no-install-recommends (Debian's minimal images
# don't pull one in). Everything else - dd, od, tr, printf, stat - is
# either already required by
# support/volumes-support/base/usr/lib/contemper/format-volumes (dd, od,
# tr, printf) or used there the same optional, best-effort way (stat),
# so this adds no dependency the helper doesn't already lean on.

# device_for_mount prints the device mounted at $1, or nothing if it
# isn't mounted at all.
device_for_mount() {
	mnt=$1
	dev=
	while read -r m_dev m_mnt _; do
		if [ "$m_mnt" = "$mnt" ]; then
			dev=$m_dev
			break
		fi
	done </proc/mounts
	[ -n "$dev" ] && printf '%s' "$dev"
}

# ext4_label_of reads the ext2/3/4 volume label straight out of device
# $1's superblock - the same dd/od approach
# support/volumes-support/base/usr/lib/contemper/format-volumes uses
# (see its own ext4_label() for why this doesn't just call e2label),
# kept here as a copy rather than a shared file since the two live in
# separate images with nothing to source it from.
ext4_label_of() {
	dev=$1
	hex=$(dd if="$dev" bs=1 skip=1144 count=16 2>/dev/null | od -An -tx1 | tr -d ' \t\n')
	label=
	rest=$hex
	while [ -n "$rest" ]; do
		byte=${rest%"${rest#??}"}
		rest=${rest#??}
		label="$label\\$(printf '%03o' "0x$byte")"
	done
	printf '%b' "$label"
}

# report_mount prints contemper-volume-mounted (or
# contemper-volume-not-mounted plus diagnostics, same as before) for
# mount point $1, and reports whether it's mounted at all; what's
# actually on it (seeded or not) is checked separately, by path, once
# this confirms there's something mounted to check at all.
report_mount() {
	mnt=$1
	dev=$(device_for_mount "$mnt")
	if [ -n "$dev" ]; then
		label=$(ext4_label_of "$dev")
		echo "contemper-volume-mounted label=${label:-<none>} dev=$dev mnt=$mnt" >/dev/console
		return 0
	fi
	echo "contemper-volume-not-mounted mnt=$mnt" >/dev/console
	{
		if command -v rc-status >/dev/null 2>&1; then
			ls -la /etc/runlevels/boot/ /etc/init.d/contemper-volumes /usr/lib/contemper/ 2>&1
			rc-status boot 2>&1
		fi
		if command -v systemctl >/dev/null 2>&1; then
			ls -la /etc/systemd/system/local-fs-pre.target.wants/ /usr/lib/contemper/ 2>&1
			systemctl status contemper-volumes.service 2>&1
		fi
		cat /etc/contemper/volumes /etc/contemper/volumes-noseed /etc/fstab 2>&1
		ls -la /dev/vd* /sys/block/*/serial 2>&1
		for s in /sys/block/*/serial; do echo "$s: $(cat "$s")"; done
	} | sed 's/^/diag: /' >/dev/console
	return 1
}

# --- /data-noseed: opted out of seeding --------------------------------
# Checked before /data: /data's "fresh"/"persisted" string is what
# deploy --expect stops the VM on, so everything else must already be on
# the console by then.
if report_mount /data-noseed; then
	# Opted out with io.contemper.volume./data-noseed.seed="false": the
	# image's own /data-noseed/seed.txt (../Containerfile bakes one in,
	# same as /data's) must never appear here, on any boot - a blank
	# format always leaves this volume empty.
	if [ -e /data-noseed/seed.txt ]; then
		echo contemper-volume-noseed-leaked >/dev/console
	else
		echo contemper-volume-noseed-ok >/dev/console
	fi
fi

# --- /data: seeded normally -------------------------------------------
if report_mount /data; then
	# The marker is written and synced before any "fresh"/"persisted"
	# string is printed: deploy --expect stops the VM as soon as that
	# string appears, so printing it first would race the write and
	# could lose it.
	marker=/data/marker
	seed=/data/seed.txt
	if [ -e "$marker" ]; then
		# Persisted (or reused-after-update) boot: the disk already
		# carried the label, so format-volumes never re-formatted or
		# re-seeded it. seed.txt must therefore still carry both the
		# original seeded line and the "guest-modified" line the first
		# boot appended - proving a guest-made change to seeded data
		# survives a reuse instead of being silently overwritten.
		if [ -f "$seed" ] && grep -q '^contemper-e2e-seed$' "$seed" && grep -q '^guest-modified$' "$seed"; then
			echo contemper-volume-modified-persisted >/dev/console
		else
			echo "contemper-volume-modified-missing content=$(cat "$seed" 2>&1)" >/dev/console
		fi
		echo contemper-volume-persisted >/dev/console
	else
		# Fresh boot: the disk was blank, so format-volumes just
		# formatted and (since /data isn't opted out) seeded it from
		# the image's own /data, which ../Containerfile bakes in as
		# seed.txt owned 1000:1000, mode 0640, in a directory owned
		# 1000:1000, mode 0750 (which the volume's root directory must
		# take over).
		attrs_ok=1
		attrs_note="stat not available, owner/mode unchecked"
		if command -v stat >/dev/null 2>&1; then
			attrs=$(stat -c '%a %u' "$seed" 2>/dev/null)
			dir_attrs=$(stat -c '%a %u' /data 2>/dev/null)
			attrs_note="attrs=$attrs dir_attrs=$dir_attrs"
			[ "$attrs" = "640 1000" ] || attrs_ok=0
			[ "$dir_attrs" = "750 1000" ] || attrs_ok=0
		fi
		if [ ! -f "$seed" ]; then
			echo contemper-volume-seed-missing >/dev/console
		elif [ "$(cat "$seed")" != "contemper-e2e-seed" ]; then
			echo "contemper-volume-seed-mismatch content=$(cat "$seed" 2>&1)" >/dev/console
		elif [ "$attrs_ok" -eq 0 ]; then
			echo "contemper-volume-seed-mismatch $attrs_note" >/dev/console
		else
			echo "contemper-volume-seeded ($attrs_note)" >/dev/console
		fi
		# Simulate a guest-made edit to the seeded file, for the
		# persisted boot above to confirm survives a reuse.
		echo guest-modified >>"$seed"
		sync
		: >"$marker"
		sync
		echo contemper-volume-fresh >/dev/console
	fi
fi
