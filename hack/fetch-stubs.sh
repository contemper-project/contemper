#!/usr/bin/env bash
# Fetches the systemd-stub UEFI stub binaries (linuxaa64.efi.stub,
# linuxx64.efi.stub) that internal/uki embeds, from Debian's
# systemd-boot-efi package, and pins their checksums in
# internal/uki/stubs/SHA256SUMS.
#
# Trust chain: HTTPS only protects the transport, not the mirror, so
# this script traces the .deb it extracts back to Debian's archive
# signature instead of trusting deb.debian.org outright:
#   1. fetch dists/<suite>/InRelease and verify its OpenPGP signature
#      against a local Debian archive keyring (never a keyring fetched
#      over the same channel it's meant to authenticate);
#   2. take the SHA256 of main/binary-<arch>/Packages(.xz|.gz) from
#      that verified InRelease, download it, and check the hash;
#   3. take the pinned systemd-boot-efi version's Filename/Size/SHA256
#      from that verified Packages index (never construct the pool
#      path by hand);
#   4. download the .deb from that Filename and check its SHA256
#      before extracting anything from it.
# An expired or revoked signing key makes step 1 fail closed (gpgv/sqv
# reject it); there is nothing further to check here.
#
# Requires: curl, gpgv (or sqv), a Debian archive keyring, dpkg-deb (or
# ar + tar), sha256sum (or shasum -a 256), gzip. xz is used to decompress
# the Packages index when available, falling back to the .gz index
# otherwise; it is not required to unpack the .deb itself.
set -euo pipefail
# Let failures inside $(...) abort too (bash >= 4.4; macOS's bash 3.2
# lacks it, where each such failure is still caught by the explicit
# checks below).
shopt -s inherit_errexit 2>/dev/null || true

# Pinned package version (needs systemd >= 256, for its UEFI stub).
# Bump this deliberately - and re-run this script - to pick up a newer
# stub. SUITE must be the Debian release whose archive currently lists
# this exact version under main/binary-<arch>/Packages; check with
#   curl -s https://deb.debian.org/debian/dists/<suite>/main/binary-<arch>/Packages.gz \
#     | gunzip | grep -A20 '^Package: systemd-boot-efi$'
# before bumping either value. If the pinned version has aged out of
# the suite's Packages index entirely (superseded by a later point
# release), fetching the specific archived version reproducibly means
# using a snapshot.debian.org timestamp instead - see the error message
# below for why this script doesn't do that automatically.
SYSTEMD_BOOT_EFI_VERSION="257.13-1~deb13u1"
SUITE="trixie"
BASE_URL="https://deb.debian.org/debian"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/internal/uki/stubs"
work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

mkdir -p "${out_dir}"

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	else
		echo "error: need sha256sum or shasum (macOS) to verify downloads" >&2
		exit 1
	fi
}

# find_keyring locates a Debian archive keyring to verify InRelease
# signatures against. DEBIAN_ARCHIVE_KEYRING overrides the search,
# which matters on macOS: there is no standard system location there,
# so copy a keyring from a Debian system's apt cache (or export one
# from https://ftp-master.debian.org/keys.html) and check its
# fingerprint out of band before pointing this at it.
find_keyring() {
	if [ -n "${DEBIAN_ARCHIVE_KEYRING:-}" ]; then
		if [ ! -f "${DEBIAN_ARCHIVE_KEYRING}" ]; then
			echo "error: DEBIAN_ARCHIVE_KEYRING=${DEBIAN_ARCHIVE_KEYRING} does not exist" >&2
			exit 1
		fi
		# gpgv looks up a relative --keyring in ~/.gnupg, so make it absolute.
		case "${DEBIAN_ARCHIVE_KEYRING}" in
		/*) printf '%s\n' "${DEBIAN_ARCHIVE_KEYRING}" ;;
		*) printf '%s/%s\n' "$(pwd)" "${DEBIAN_ARCHIVE_KEYRING}" ;;
		esac
		return
	fi
	if [ -f /usr/share/keyrings/debian-archive-keyring.gpg ]; then
		printf '%s\n' /usr/share/keyrings/debian-archive-keyring.gpg
		return
	fi
	echo "error: no Debian archive keyring found." >&2
	echo "  Debian/Ubuntu: apt install debian-archive-keyring" >&2
	echo "  Elsewhere (e.g. macOS): set DEBIAN_ARCHIVE_KEYRING=/path/to/debian-archive-keyring.gpg" >&2
	echo "  (verify its fingerprint out of band; never trust a copy fetched over the channel" >&2
	echo "  this script is trying to authenticate)" >&2
	exit 1
}

# verify_release checks inline-signed file $1's OpenPGP signature
# against keyring $2 and writes only the signed content to $3. Everything
# after this reads $3, never $1: text outside the signed block of an
# inline-signed file is not covered by the signature. An expired,
# revoked, or otherwise untrusted key makes this fail (nonzero exit),
# which - with set -e - aborts the whole script.
verify_release() {
	if command -v gpgv >/dev/null 2>&1; then
		gpgv --keyring "$2" --output "$3" "$1"
	elif command -v sqv >/dev/null 2>&1; then
		sqv --keyring "$2" --message --output "$3" "$1"
	else
		echo "error: need gpgv or sqv to verify the Debian archive signature" >&2
		echo "  Debian/Ubuntu: apt install gpgv" >&2
		echo "  macOS (Homebrew): brew install gnupg" >&2
		exit 1
	fi
}

# release_sha256_for prints the SHA256 of path $2 (relative to
# dists/<suite>/, e.g. main/binary-arm64/Packages.xz) as listed in the
# verified InRelease file $1, or fails if it's not listed.
release_sha256_for() {
	awk -v want="$2" '
		/^SHA256:$/ { insha = 1; next }
		insha && /^[^ ]/ { insha = 0 }
		insha && $3 == want { print $1; found = 1; exit }
		END { exit(found ? 0 : 1) }
	' "$1"
}

# packages_entry_for prints the Filename, SHA256 and Size (one per
# line, in that order) of the systemd-boot-efi stanza matching arch $2
# and version $3 in Packages file $1, or fails if there is no such
# stanza (e.g. the pinned version isn't in this suite's archive).
packages_entry_for() {
	awk -v want_arch="$2" -v want_ver="$3" '
		BEGIN { RS = ""; FS = "\n" }
		{
			pkg = ""; ver = ""; arch = ""; fname = ""; sha = ""; sz = ""
			for (i = 1; i <= NF; i++) {
				line = $i
				if (line ~ /^Package: /)           { pkg   = substr(line, 10) }
				else if (line ~ /^Version: /)       { ver   = substr(line, 10) }
				else if (line ~ /^Architecture: /)  { arch  = substr(line, 15) }
				else if (line ~ /^Filename: /)      { fname = substr(line, 11) }
				else if (line ~ /^SHA256: /)        { sha   = substr(line, 9) }
				else if (line ~ /^Size: /)          { sz    = substr(line, 7) }
			}
			if (pkg == "systemd-boot-efi" && arch == want_arch && ver == want_ver) {
				print fname; print sha; print sz
				found = 1
				exit
			}
		}
		END { exit(found ? 0 : 1) }
	' "$1"
}

# fetch_checked downloads url $1 to path $2 and aborts unless its
# SHA256 matches $3.
fetch_checked() {
	local url="$1" dest="$2" expected="$3" actual
	curl --proto =https -fsSL -o "${dest}" "${url}"
	actual="$(sha256_file "${dest}")"
	if [ "${actual}" != "${expected}" ]; then
		echo "error: ${url}" >&2
		echo "  SHA256 mismatch: got ${actual}, expected ${expected}" >&2
		exit 1
	fi
}

keyring="$(find_keyring)"

echo "fetching dists/${SUITE}/InRelease..." >&2
signed_inrelease="${work_dir}/InRelease.signed"
inrelease="${work_dir}/InRelease"
curl --proto =https -fsSL -o "${signed_inrelease}" "${BASE_URL}/dists/${SUITE}/InRelease"
verify_release "${signed_inrelease}" "${keyring}" "${inrelease}"
echo "verified dists/${SUITE}/InRelease signature (keyring: ${keyring})" >&2

# fetch_packages downloads and hash-checks the arch's Packages index
# (preferring .xz, falling back to .gz when xz isn't available to
# decompress it) and echoes the path to the decompressed file.
fetch_packages() {
	local arch="$1" ext rel_path expected dl out
	if command -v xz >/dev/null 2>&1; then
		ext="xz"
	else
		ext="gz"
	fi
	rel_path="main/binary-${arch}/Packages.${ext}"
	expected="$(release_sha256_for "${inrelease}" "${rel_path}")" || {
		echo "error: ${rel_path} is not listed in the verified dists/${SUITE}/InRelease" >&2
		exit 1
	}
	dl="${work_dir}/Packages-${arch}.${ext}"
	fetch_checked "${BASE_URL}/dists/${SUITE}/${rel_path}" "${dl}" "${expected}"
	out="${work_dir}/Packages-${arch}"
	if [ "${ext}" = "xz" ]; then
		xz -dc "${dl}" >"${out}"
	else
		gunzip -c "${dl}" >"${out}"
	fi
	printf '%s\n' "${out}"
}

# arch: Debian arch name, stub: the .stub file name inside the package.
fetch_arch() {
	local arch="$1" stub="$2"
	local packages_file entry pkg_filename pkg_sha256 pkg_size
	local deb extract src

	echo "fetching main/binary-${arch}/Packages..." >&2
	packages_file="$(fetch_packages "${arch}")"

	entry="$(packages_entry_for "${packages_file}" "${arch}" "${SYSTEMD_BOOT_EFI_VERSION}")" || {
		echo "error: systemd-boot-efi ${SYSTEMD_BOOT_EFI_VERSION} (${arch}) is not in" >&2
		echo "  dists/${SUITE}/main/binary-${arch}/Packages." >&2
		echo "  It may have been superseded in ${SUITE} since this pin was made; bump" >&2
		echo "  SYSTEMD_BOOT_EFI_VERSION (and SUITE, if needed) to a version currently in the" >&2
		echo "  archive. To instead reproduce this exact pinned version, fetch it from a" >&2
		echo "  matching snapshot.debian.org timestamp - snapshot.debian.org also serves a" >&2
		echo "  signed InRelease per timestamp, so the same verification applies - see" >&2
		echo "  https://snapshot.debian.org/package/systemd/${SYSTEMD_BOOT_EFI_VERSION}/" >&2
		exit 1
	}
	pkg_filename="$(printf '%s\n' "${entry}" | sed -n '1p')"
	pkg_sha256="$(printf '%s\n' "${entry}" | sed -n '2p')"
	pkg_size="$(printf '%s\n' "${entry}" | sed -n '3p')"

	echo "fetching ${pkg_filename}..." >&2
	deb="${work_dir}/systemd-boot-efi_${arch}.deb"
	fetch_checked "${BASE_URL}/${pkg_filename}" "${deb}" "${pkg_sha256}"
	actual_size="$(wc -c <"${deb}" | tr -d ' ')"
	if [ "${actual_size}" != "${pkg_size}" ]; then
		echo "error: ${pkg_filename} size mismatch: got ${actual_size}, expected ${pkg_size}" >&2
		exit 1
	fi

	extract="${work_dir}/extract-${arch}"
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

	src="${extract}/usr/lib/systemd/boot/efi/${stub}"
	if [ ! -f "${src}" ]; then
		echo "error: ${stub} not found in ${pkg_filename}" >&2
		exit 1
	fi
	cp "${src}" "${out_dir}/${stub}"
}

fetch_arch arm64 linuxaa64.efi.stub
fetch_arch amd64 linuxx64.efi.stub

(
	cd "${out_dir}"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum linuxaa64.efi.stub linuxx64.efi.stub > SHA256SUMS
	else
		shasum -a 256 linuxaa64.efi.stub linuxx64.efi.stub > SHA256SUMS
	fi
)

echo "wrote ${out_dir}/linuxaa64.efi.stub, ${out_dir}/linuxx64.efi.stub, and SHA256SUMS" >&2
