#!/bin/sh
# Shared logic behind hack/e2e-volumes.sh's boot check. Both init-system
# integrations run this same script: the OpenRC one
# (zzz-contemper-volume-test.start) execs it directly, and the systemd
# one (contemper-volume-test.service) points ExecStart at it, so the
# check itself is never duplicated between the two.
#
# Prints whether /data is mounted, and on which device carrying which
# label, then prints contemper-volume-fresh (and leaves a marker behind)
# on a volume's first boot, or contemper-volume-persisted if that marker
# is already there.
#
# POSIX sh, no awk: the mount lookup below uses a plain `read` loop
# rather than `awk '$2 == "/data"'`, since awk isn't guaranteed present
# on an image built --no-install-recommends (Debian's minimal images
# don't pull one in). Everything else - dd, od, tr, printf - is the same
# toolset support/volumes-support/base/usr/lib/contemper/format-volumes
# already requires, so this adds no new dependency for either example.
dev=
while read -r m_dev m_mnt _; do
	if [ "$m_mnt" = "/data" ]; then
		dev=$m_dev
		break
	fi
done </proc/mounts

if [ -n "$dev" ]; then
	# e2label lives in e2fsprogs-extra, which neither example installs
	# (only plain e2fsprogs, for fsck.ext4), so read the label straight
	# out of the ext2/3/4 superblock instead, the same way
	# support/volumes-support/base/usr/lib/contemper/format-volumes does:
	# s_volume_name is 16 bytes, NUL-padded, at offset 1024+120.
	hex=$(dd if="$dev" bs=1 skip=1144 count=16 2>/dev/null | od -An -tx1 | tr -d ' \t\n')
	label=
	rest=$hex
	while [ -n "$rest" ]; do
		byte=${rest%"${rest#??}"}
		rest=${rest#??}
		label="$label\\$(printf '%03o' "0x$byte")"
	done
	label=$(printf '%b' "$label")
	echo "contemper-volume-mounted label=${label:-<none>} dev=$dev" >/dev/console
else
	echo "contemper-volume-not-mounted" >/dev/console
	# Diagnostics for a failed run: is the helper installed and enabled,
	# and did the init system start it? Only the commands that exist on
	# this image's init system produce anything.
	{
		if command -v rc-status >/dev/null 2>&1; then
			ls -la /etc/runlevels/boot/ /etc/init.d/contemper-volumes /usr/lib/contemper/ 2>&1
			rc-status boot 2>&1
		fi
		if command -v systemctl >/dev/null 2>&1; then
			ls -la /etc/systemd/system/local-fs-pre.target.wants/ /usr/lib/contemper/ 2>&1
			systemctl status contemper-volumes.service 2>&1
		fi
		cat /etc/contemper/volumes /etc/fstab 2>&1
		ls -la /dev/vd* /sys/block/*/serial 2>&1
		for s in /sys/block/*/serial; do echo "$s: $(cat "$s")"; done
	} | sed 's/^/diag: /' >/dev/console
fi

# The marker is written and synced before "fresh" is printed: deploy
# --expect stops the VM as soon as that string appears, so printing it
# first would race the write and could lose it.
marker=/data/marker
if [ -e "$marker" ]; then
	echo contemper-volume-persisted >/dev/console
else
	: >"$marker"
	sync
	echo contemper-volume-fresh >/dev/console
fi
