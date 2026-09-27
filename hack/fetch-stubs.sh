#!/usr/bin/env bash
# Fetches the systemd-stub UEFI stub binaries (linuxaa64.efi.stub,
# linuxx64.efi.stub) that internal/uki embeds, from Debian's
# systemd-boot-efi package, and pins their checksums in
# internal/uki/stubs/SHA256SUMS.
#
# Requires: curl, dpkg-deb (or ar + tar), sha256sum.
set -euo pipefail

# Pinned package version (needs systemd >= 256, for its UEFI stub).
# Bump this deliberately - and re-run this script - to pick up a newer
# stub.
SYSTEMD_BOOT_EFI_VERSION="257.13-1~deb13u1"
MIRROR="https://deb.debian.org/debian/pool/main/s/systemd"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/internal/uki/stubs"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

mkdir -p "${out_dir}"

# arch: Debian arch name, stub: the .stub file name inside the package.
fetch_arch() {
	local arch="$1" stub="$2"
	local deb="${work_dir}/systemd-boot-efi_${arch}.deb"
	local extract="${work_dir}/extract-${arch}"

	echo "fetching systemd-boot-efi ${SYSTEMD_BOOT_EFI_VERSION} (${arch})..." >&2
	curl --proto =https -fsSL -o "${deb}" \
		"${MIRROR}/systemd-boot-efi_${SYSTEMD_BOOT_EFI_VERSION}_${arch}.deb"

	mkdir -p "${extract}"
	if command -v dpkg-deb >/dev/null 2>&1; then
		dpkg-deb -x "${deb}" "${extract}"
	else
		# Fallback for hosts without dpkg-deb (e.g. macOS): a .deb is an
		# ar archive containing control.tar.* and data.tar.*.
		(cd "${extract}" && ar x "${deb}" data.tar.zst 2>/dev/null \
			|| ar x "${deb}" data.tar.xz 2>/dev/null \
			|| ar x "${deb}" data.tar.gz)
		(cd "${extract}" && tar xf data.tar.* )
	fi

	local src="${extract}/usr/lib/systemd/boot/efi/${stub}"
	if [ ! -f "${src}" ]; then
		echo "error: ${stub} not found in systemd-boot-efi_${arch}.deb" >&2
		exit 1
	fi
	cp "${src}" "${out_dir}/${stub}"
}

fetch_arch arm64 linuxaa64.efi.stub
fetch_arch amd64 linuxx64.efi.stub

(
	cd "${out_dir}"
	sha256sum linuxaa64.efi.stub linuxx64.efi.stub > SHA256SUMS
)

echo "wrote ${out_dir}/linuxaa64.efi.stub, ${out_dir}/linuxx64.efi.stub, and SHA256SUMS" >&2
