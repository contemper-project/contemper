#!/usr/bin/env bash
# End-to-end bootloader-image test: build examples/debian-grub, derive a
# test image from it (hack/e2e-bootloader/Containerfile) that carries a
# second kernel as offline packages, convert it (the image declares
# io.contemper.boot=bootloader, so the disk gets the image's own GRUB on
# a larger ESP and a guest mount of it at /boot/efi), and boot it with
# `contemper deploy --to local-qemu`. Inside the guest:
#
#  1. first boot: the image's own first-boot unit installs GRUB and
#     generates grub.cfg; then a unit checks that, records the running
#     kernel in a marker on the ESP, installs the second kernel with
#     dpkg, checks that the kernel hook regenerated /boot/grub/grub.cfg
#     with an entry for it, and reboots;
#  2. second boot (QEMU restarts the guest in the same process, keeping
#     the throwaway disk overlay): the unit checks that the new kernel
#     runs and that the ESP is mounted with the marker intact, reinstalls
#     the bootloader with dpkg-reconfigure of the GRUB package (the
#     path a package upgrade takes), checks that the shim on the ESP is
#     the installed one, and reboots;
#  3. third boot: the unit checks that the guest still boots the new
#     kernel from the reinstalled bootloader and prints
#     contemper-e2e-kernel-updated.
#
# With --secure-boot, the derived image also carries
# io.contemper.secure-boot="true", the VM boots Secure Boot firmware, and
# the guest checks on every boot that the firmware reports Secure Boot
# on (and, without the flag, that it reports it off). The three boots then
# cover booting shim, signed GRUB and a signed kernel, the offline update
# of the signed cloud kernel, and the bootloader reinstall.
#
# Any failure in the guest prints contemper-e2e-fail: <reason> on the
# console and powers the VM off; this script reports it. The serial log
# holds all three boots; a GRUB "error: " line in it (a module or file it
# could not load) fails the test too, as does GRUB never having printed
# anything there.
#
# Usage: hack/e2e-bootloader.sh [--timeout DURATION] [--secure-boot]
#
# Requires: go, podman or docker (CONTAINER_ENGINE selects one
# explicitly), e2fsprogs (mkfs.ext4, debugfs, e2fsck), qemu-img,
# qemu-system-<arch> and UEFI firmware for it (with --secure-boot, OVMF or
# AAVMF firmware with Microsoft's keys enrolled, e.g. Debian's ovmf package). Output, including the
# serial log, goes to _out/bootloader/.
#
# Set CONTEMPER_E2E_COVERDIR to a directory to build contemper with Go's
# source-code coverage instrumentation and collect the integration
# coverage from every invocation below into it (see `go help testflag`'s
# GOCOVERDIR, and `go tool covdata`). Unset, nothing here changes.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO}/_out/bootloader"
TIMEOUT="600s"
INSTALLED_MARKER="contemper-e2e-kernel-installed"
REINSTALLED_MARKER="contemper-e2e-bootloader-reinstalled"
SUCCESS_MARKER="contemper-e2e-kernel-updated"
FAIL_MARKER="contemper-e2e-fail"
IMAGE="contemper-example-debian-grub:dev"
DERIVED_IMAGE="contemper-example-bootloader-update:e2e"
SECURE_BOOT=false

while [ $# -gt 0 ]; do
	case "$1" in
	--timeout)
		TIMEOUT="$2"
		shift 2
		;;
	--secure-boot)
		SECURE_BOOT=true
		shift
		;;
	*)
		echo "e2e-bootloader.sh: unknown argument: $1" >&2
		exit 2
		;;
	esac
done

engine="${CONTAINER_ENGINE:-}"
if [ -z "${engine}" ]; then
	for candidate in podman docker; do
		if command -v "${candidate}" >/dev/null; then
			engine="${candidate}"
			break
		fi
	done
fi
if [ -z "${engine}" ]; then
	echo "e2e-bootloader.sh: neither podman nor docker found; set CONTAINER_ENGINE" >&2
	exit 1
fi
command -v go >/dev/null || { echo "e2e-bootloader.sh: go not found" >&2; exit 1; }

COVERDIR="${CONTEMPER_E2E_COVERDIR:-}"
if [ -n "${COVERDIR}" ]; then
	mkdir -p "${COVERDIR}"
	# Absolute, since Go resolves a relative GOCOVERDIR against each
	# process's own working directory.
	COVERDIR="$(cd "${COVERDIR}" && pwd)"
	export GOCOVERDIR="${COVERDIR}"
fi

platform="$(go env GOOS)-$(go env GOARCH)"
contemper="${REPO}/bin/${platform}/contemper"
echo "==> go build (${platform})" >&2
if [ -n "${COVERDIR}" ]; then
	(cd "${REPO}" && go build -cover -coverpkg=./... -o "bin/${platform}/contemper" ./cmd/contemper)
else
	(cd "${REPO}" && go build -o "bin/${platform}/contemper" ./cmd/contemper)
fi

rm -rf "${OUT}"
mkdir -p "${OUT}"

echo "==> ${engine} build examples/debian-grub" >&2
"${engine}" build -t "${IMAGE}" -f "${REPO}/examples/debian-grub/Containerfile" "${REPO}/examples/debian-grub"

echo "==> ${engine} build hack/e2e-bootloader (from ${IMAGE}, secure boot ${SECURE_BOOT})" >&2
"${engine}" build -t "${DERIVED_IMAGE}" --build-arg BASE_IMAGE="${IMAGE}" --build-arg SECURE_BOOT="${SECURE_BOOT}" -f "${REPO}/hack/e2e-bootloader/Containerfile" "${REPO}/hack/e2e-bootloader"

# podman can write an OCI archive; docker save only writes its own format.
case "${engine}" in
podman)
	"${engine}" save --format oci-archive -o "${OUT}/image.tar" "${DERIVED_IMAGE}"
	source_ref="oci-archive:${OUT}/image.tar"
	;;
*)
	"${engine}" save -o "${OUT}/image.tar" "${DERIVED_IMAGE}"
	source_ref="docker-archive:${OUT}/image.tar"
	;;
esac

echo "==> contemper convert" >&2
bundle="$("${contemper}" convert --target qemu "${source_ref}" -o "${OUT}")"

# No XDG_STATE_HOME override needed: no volumes, so deploy keeps no state.
log="${OUT}/serial.log"
echo "==> contemper deploy --to local-qemu (expecting ${SUCCESS_MARKER})" >&2
status=0
"${contemper}" deploy --to local-qemu "${bundle}" \
	--expect "${SUCCESS_MARKER}" \
	--timeout "${TIMEOUT}" \
	--serial-log "${log}" || status=$?

if grep -q "${FAIL_MARKER}" "${log}" 2>/dev/null; then
	echo "e2e-bootloader.sh: the guest reported a failure:" >&2
	grep "${FAIL_MARKER}" "${log}" >&2
	exit 1
fi
# GRUB prints "error: ..." lines on the console when it cannot load a
# module or file, e.g. bli.mod before grub-install has ever run. Grep a
# copy without ANSI escape sequences and carriage returns, since GRUB's
# menu is drawn with them and they can precede the text on a line. Both
# are generated with printf: BSD sed knows neither \033 nor \r.
plain="${OUT}/serial.plain.log"
LC_ALL=C sed -e "$(printf 's/\033\\[[0-9;?]*[A-Za-z]//g')" -e "$(printf 's/\r//g')" "${log}" >"${plain}" 2>/dev/null || : >"${plain}"
# With Secure Boot on, the signed GRUB refuses every module Debian's
# generated grub.cfg loads from /boot/grub (insmod bli and others), and
# prints this line for each; it is harmless and booting continues, so
# exactly this line is tolerated in that mode, and nothing otherwise.
tolerated='error: prohibited by secure boot policy.'
if [ "${SECURE_BOOT}" = true ]; then
	errors="$(grep '^error: ' "${plain}" | grep -vxF "${tolerated}" || true)"
	ignored="$(grep -cxF "${tolerated}" "${plain}" || true)"
	echo "e2e-bootloader.sh: tolerated ${ignored} \"${tolerated}\" lines (modules refused under Secure Boot)" >&2
else
	errors="$(grep '^error: ' "${plain}" || true)"
fi
if [ -n "${errors}" ]; then
	echo "e2e-bootloader.sh: GRUB reported an error on the console:" >&2
	echo "${errors}" >&2
	exit 1
fi
if [ "${status}" -ne 0 ]; then
	echo "e2e-bootloader.sh: deploy failed (exit ${status}); serial log: ${log}" >&2
	exit "${status}"
fi
# GRUB's menu reaches the serial console: through its own serial terminal
# on amd64, through the firmware console on arm64. Without it the checks
# above prove nothing about GRUB.
if ! grep -q 'GNU GRUB' "${plain}"; then
	echo "e2e-bootloader.sh: no GRUB output reached the console (serial log: ${log})" >&2
	exit 1
fi
# The success marker is printed by a unit that multi-user.target waits
# for, so contemper-boot-ok may not have been printed yet when deploy
# returns; only the intermediate markers are checked besides it.
for marker in "${INSTALLED_MARKER}" "${REINSTALLED_MARKER}"; do
	if ! grep -q "${marker}" "${log}"; then
		echo "e2e-bootloader.sh: ${marker} never appeared on the console (serial log: ${log})" >&2
		exit 1
	fi
done

echo "==> e2e-bootloader OK (secure boot ${SECURE_BOOT}): kernel installed in the guest, grub.cfg regenerated, bootloader reinstalled, new kernel running after both reboots" >&2
