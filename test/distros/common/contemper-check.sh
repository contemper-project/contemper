#!/bin/sh
# In-guest boot check for the distribution test images. Prints
# "contemper-boot-ok" on the serial console only when the root
# filesystem looks healthy, and otherwise a distinct line
#
#   contemper-check-failed: <reason>
#
# so that hack/e2e.sh can stop waiting as soon as a check fails. The
# checks are:
#   1. root is an ext4 mount from LABEL=contemper-root, mounted read-write
#   2. no failed fsck unit/service for the root filesystem
#   3. no ext4 errors or "unsupported feature" messages in the kernel log
# POSIX sh only: this also runs on busybox.

con=/dev/console

say() { echo "contemper-check: $*" >"$con"; }

fail() {
	echo "contemper-check-failed: $*" >"$con"
	exit 1
}

# 1. Root mount. /proc/mounts lists the initramfs "rootfs" entry first on
# some kernels; take the last entry for "/".
root_line="$(awk '$2 == "/" { l = $0 } END { print l }' /proc/mounts)"
[ -n "$root_line" ] || fail "no root entry in /proc/mounts"
read -r root_src _ root_type root_opts _ <<EOT
$root_line
EOT
[ "$root_type" = ext4 ] || fail "root is $root_type on $root_src, want ext4"
case ",$root_opts," in
*,rw,*) ;;
*) fail "root on $root_src is not mounted read-write (options: $root_opts)" ;;
esac
# contemper always puts root=LABEL=contemper-root first on the kernel
# command line; make sure the running root really came from there. When
# udev (or mdev) created the by-label link, it must name the mounted
# device too.
case " $(cat /proc/cmdline) " in
*" root=LABEL=contemper-root "*) ;;
*) fail "kernel command line has no root=LABEL=contemper-root: $(cat /proc/cmdline)" ;;
esac
label_dev=/dev/disk/by-label/contemper-root
if [ -e "$label_dev" ] && [ -e "$root_src" ]; then
	[ "$(readlink -f "$label_dev")" = "$(readlink -f "$root_src")" ] ||
		fail "root is mounted from $root_src, but LABEL=contemper-root is $(readlink -f "$label_dev")"
fi
say "root $root_src ext4 $root_opts"

# 2. fsck of the root filesystem. systemd runs systemd-fsck-root.service
# (in the initrd for dracut images, in the real root for others); on
# OpenRC the fsck service has to be in the boot runlevel.
if command -v systemctl >/dev/null 2>&1; then
	state="$(systemctl is-failed systemd-fsck-root.service 2>/dev/null || true)"
	[ "$state" != failed ] || fail "systemd-fsck-root.service failed"
	failed_fsck="$(systemctl --no-legend --plain --failed 2>/dev/null | awk '$1 ~ /fsck/ { print $1 }')"
	[ -z "$failed_fsck" ] || fail "failed fsck unit: $failed_fsck"
	say "fsck: systemd-fsck-root.service is $state"
elif command -v rc-service >/dev/null 2>&1; then
	state="$(rc-service fsck status 2>&1 || true)"
	case "$state" in
	*crashed* | *stopped*) fail "OpenRC fsck service: $state" ;;
	esac
	say "fsck: OpenRC $state"
fi

# 3. Kernel log.
if command -v dmesg >/dev/null 2>&1; then
	klog="$(dmesg 2>/dev/null || true)"
else
	klog="$(journalctl -k -b --no-pager 2>/dev/null || true)"
fi
bad="$(printf '%s\n' "$klog" | grep -iE 'EXT4-fs.*(error|unsupported|couldn.t mount|mounting fs with errors)|unknown/unsupported|remounting filesystem read-only' | head -n 3)"
[ -z "$bad" ] || fail "kernel log: $(printf '%s' "$bad" | tr '\n' '|')"

echo contemper-boot-ok >"$con"
