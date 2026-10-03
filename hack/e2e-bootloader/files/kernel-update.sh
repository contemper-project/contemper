#!/bin/sh
# Boot-time driver of the in-guest kernel update test (see
# hack/e2e-bootloader.sh and ../Containerfile). Runs from
# contemper-kernel-update.service on every boot and tells the three boots
# apart by a stage file on the root filesystem (not on the ESP, whose
# state is part of what is being tested).
#
# First boot: check that the image's first-boot unit ran grub-install
# and update-grub (core.efi and the unit's stamp exist, grub.cfg is
# Debian's generated one),
# remember the running kernel in a marker on the ESP, install the second
# kernel from the offline package directory, check the kernel package
# repointed /vmlinuz and made an initrd, and that its hook regenerated
# /boot/grub/grub.cfg with an entry for it, reboot.
# Second boot: check the new kernel runs, the ESP is mounted again and
# the marker survived, then reinstall the bootloader through the real
# package path (dpkg-reconfigure of grub-efi-<arch>, whose postinst
# only runs grub-install because the first-boot unit left core.efi
# behind) after deleting the bootloader files from the ESP, check they
# were written back (shim, GRUB, the grub.cfg stub), reboot.
# Third boot: check the new kernel still runs after the reinstall, then
# print the success marker.
#
# On every boot, the SecureBoot EFI variable is read and printed, and the
# boot fails if it differs from the state the image was built to expect
# (the secure-boot file next to the offline packages). With Secure Boot
# on, the three boots therefore cover shim, signed GRUB and a signed
# kernel, the offline update of the signed cloud kernel, and the
# dpkg-reconfigure reinstall.
#
# Every outcome is a line on the serial console. A failure prints
# contemper-e2e-fail and powers the VM off, so the host side sees QEMU
# exit instead of waiting out its timeout.
set -u

data=/usr/local/share/contemper-e2e
state=/var/lib/contemper-e2e
esp=/boot/efi
marker="${esp}/contemper-e2e-marker"

say() { echo "$*" >/dev/console; }
fail() {
	say "contemper-e2e-fail: $*"
	systemctl --no-block poweroff
	exit 1
}

# The unit's WantsMountsFor= orders this after the ESP mount but does not
# require it to succeed; check here, since an ESP that is not mounted (or
# only read-only) is exactly the failure to report.
grep -q " ${esp} vfat rw[ ,]" /proc/mounts || fail "${esp} is not mounted read-write as vfat"

# The variable is 4 attribute bytes followed by one data byte, 1 when
# Secure Boot is on.
sb_var=/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c
want_sb="$(cat "${data}/secure-boot" 2>/dev/null || true)"
case "${want_sb}" in
true | false) ;;
*) fail "${data}/secure-boot holds \"${want_sb}\", expected true or false" ;;
esac
# Firmware without Secure Boot support does not define the variable at
# all, which means off. To still catch efivarfs not being mounted (where
# the variable would be missing too), require the directory to be a
# mounted, non-empty efivarfs first.
efivars=/sys/firmware/efi/efivars
grep -q " ${efivars} efivarfs " /proc/mounts || fail "${efivars} is not a mounted efivarfs"
[ -n "$(ls -A "${efivars}" 2>/dev/null)" ] || fail "${efivars} is empty"
if [ -e "${sb_var}" ]; then
	[ -r "${sb_var}" ] || fail "cannot read the SecureBoot EFI variable at ${sb_var}"
	case "$(od -An -v -tu1 "${sb_var}" | tr -s ' \n' ' ' | awk '{print $NF}')" in
	1) got_sb=true ;;
	0) got_sb=false ;;
	*) fail "the SecureBoot EFI variable has an unexpected value: $(od -An -v -tu1 "${sb_var}" | tr -s ' \n' ' ')" ;;
	esac
else
	got_sb=false
fi
say "contemper-e2e: secure boot ${got_sb} (expected ${want_sb})"
[ "${got_sb}" = "${want_sb}" ] || fail "secure boot is ${got_sb}, expected ${want_sb}"

case "$(dpkg --print-architecture)" in
amd64)
	arch=amd64
	target=x86_64-efi
	boot=BOOTX64.EFI
	shim=/usr/lib/shim/shimx64.efi.signed
	grub=/usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed
	grubname=grubx64.efi
	;;
arm64)
	arch=arm64
	target=arm64-efi
	boot=BOOTAA64.EFI
	shim=/usr/lib/shim/shimaa64.efi.signed
	grub=/usr/lib/grub/arm64-efi-signed/grubaa64.efi.signed
	grubname=grubaa64.efi
	;;
*) fail "unsupported architecture $(dpkg --print-architecture)" ;;
esac

new_kernel="$(cat "${data}/new-kernel")"
running="$(uname -r)"

if [ ! -e "${state}/stage" ]; then
	# contemper-grub-firstboot.service (from the image) ran before this
	# unit: GRUB is installed and the real configuration generated.
	[ -e "/boot/grub/${target}/core.efi" ] ||
		fail "/boot/grub/${target}/core.efi is missing: the first-boot grub-install did not run"
	[ -e /var/lib/contemper-grub-firstboot/done ] ||
		fail "the first-boot unit's stamp is missing: it did not finish"
	grep -q '### BEGIN /etc/grub.d/10_linux' /boot/grub/grub.cfg ||
		fail "/boot/grub/grub.cfg is still the bootstrap config: the first-boot update-grub did not run"
	say "contemper-e2e: first-boot GRUB install present (core.efi, stamp, generated grub.cfg)"

	mkdir -p "${state}"
	echo "${running}" >"${state}/first-kernel"
	echo "${running}" >"${marker}" || fail "cannot write the marker onto ${esp}"
	sync
	say "contemper-e2e: first boot on ${running}, installing ${new_kernel}"

	DEBIAN_FRONTEND=noninteractive dpkg -i "${data}"/debs/*.deb >/dev/console 2>&1 ||
		fail "dpkg -i of the second kernel failed"

	# The kernel package is what keeps these current; the static
	# grub.cfg depends on both.
	case "$(readlink -f /vmlinuz)" in
	*"/vmlinuz-${new_kernel}") ;;
	*) fail "/vmlinuz does not point at ${new_kernel} after the install: $(readlink -f /vmlinuz)" ;;
	esac
	case "$(readlink -f /initrd.img)" in
	*"/initrd.img-${new_kernel}") ;;
	*) fail "/initrd.img does not point at ${new_kernel} after the install: $(readlink -f /initrd.img)" ;;
	esac

	# The kernel hook zz-update-grub runs update-grub because
	# grub.cfg exists and regenerates it with the new kernel.
	grub_cfg=/boot/grub/grub.cfg
	grep -q '### BEGIN /etc/grub.d/10_linux' "${grub_cfg}" ||
		fail "${grub_cfg} was not regenerated by update-grub (no 10_linux section): the kernel hook did not run"
	grep "menuentry " "${grub_cfg}" | grep -q "${new_kernel}" ||
		fail "${grub_cfg} has no menuentry for ${new_kernel}"
	say "contemper-e2e: ${grub_cfg} regenerated with an entry for ${new_kernel}"

	echo installed >"${state}/stage"
	sync
	say "contemper-e2e-kernel-installed: ${new_kernel}"
	systemctl --no-block reboot
	exit 0
fi

first="$(cat "${state}/first-kernel" 2>/dev/null || true)"
[ "${running}" = "${new_kernel}" ] || fail "boot on ${running}, expected ${new_kernel}"

if [ "$(cat "${state}/stage")" = installed ]; then
	[ -f "${marker}" ] || fail "marker is gone from ${esp} after the reboot"
	[ "$(cat "${marker}")" = "${first}" ] || fail "marker on ${esp} has unexpected content: $(cat "${marker}")"
	say "contemper-e2e-kernel-running: ${first} -> ${running}, ${esp} marker intact"

	# Under Secure Boot the signed GRUB cannot load modules from disk, so
	# every insmod in the generated config prints an error. List them
	# once, so the log shows which modules are refused.
	if [ "${want_sb}" = true ]; then
		say "contemper-e2e: insmod lines in /boot/grub/grub.cfg:"
		grep -E '^[[:space:]]*insmod[[:space:]]' /boot/grub/grub.cfg >/dev/console || true
	fi

	# The real package path: the grub-efi-<arch> postinst reruns
	# grub-install (with --force-extra-removable and --no-nvram from the
	# debconf answers the image preseeds) because core.efi exists.
	# grub-install picks the signed shim and GRUB itself since
	# shim-signed is installed.
	# Delete what the reinstall must write back, so that finding it
	# afterwards proves the reinstall ran. core.efi stays: the postinst
	# only runs grub-install while it exists.
	rm -f "${esp}/EFI/BOOT/${boot}" "${esp}/EFI/BOOT/${grubname}" "${esp}/EFI/debian/grub.cfg"
	sync
	say "contemper-e2e: dpkg-reconfigure grub-efi-${arch}"
	DEBIAN_FRONTEND=noninteractive dpkg-reconfigure -f noninteractive "grub-efi-${arch}" >/dev/console 2>&1 ||
		fail "dpkg-reconfigure grub-efi-${arch} failed"
	sync
	say "contemper-e2e: ${esp}/EFI/BOOT after the reinstall: $(find "${esp}/EFI/BOOT" -mindepth 1 -printf '%f ')"
	say "contemper-e2e: ${esp}/EFI/debian after the reinstall: $(find "${esp}/EFI/debian" -mindepth 1 -printf '%f ')"
	[ -f "${esp}/EFI/BOOT/${boot}" ] || fail "${esp}/EFI/BOOT/${boot} was not written back by the reinstall"
	cmp -s "${esp}/EFI/BOOT/${boot}" "${shim}" ||
		fail "${esp}/EFI/BOOT/${boot} differs from the installed shim ${shim} after the reinstall"
	[ -f "${esp}/EFI/BOOT/${grubname}" ] || fail "${esp}/EFI/BOOT/${grubname} was not written back by the reinstall"
	cmp -s "${esp}/EFI/BOOT/${grubname}" "${grub}" ||
		fail "${esp}/EFI/BOOT/${grubname} differs from the installed signed GRUB ${grub} after the reinstall"
	[ -f "${esp}/EFI/debian/grub.cfg" ] || fail "${esp}/EFI/debian/grub.cfg was not written back by the reinstall"
	echo reinstalled >"${state}/stage"
	sync
	say "contemper-e2e-bootloader-reinstalled: ${boot} is ${shim}, ${grubname} is ${grub}, grub.cfg stub present"
	systemctl --no-block reboot
	exit 0
fi

grep -q " ${esp} vfat rw[ ,]" /proc/mounts || fail "${esp} is not mounted read-write after the bootloader reinstall"
say "contemper-e2e-kernel-updated: ${first} -> ${running}, booted again after the bootloader reinstall"
