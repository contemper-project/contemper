#!/usr/bin/env bash
# End-to-end support-image variant test: build the Alpine example image,
# a dummy support image with two branches ("init-system" and "extras")
# and one variant image, push them to a local registry at
# localhost:5555 (E2E_REGISTRY overrides), convert with `contemper convert --target qemu --support
# <ref>`, assert the resolved variants recorded in contemper.json, then
# boot the bundle exactly like hack/e2e.sh but waiting for the winning
# variant's own marker. It also exercises two failure cases (an
# ambiguous branch and one with no match and no default) without
# booting anything.
#
# Usage: hack/e2e-variants.sh [--timeout DURATION]
#
# Requires: go, podman or docker (CONTAINER_ENGINE selects one
# explicitly), curl, e2fsprogs (mkfs.ext4, debugfs, e2fsck), qemu-img,
# qemu-system-<arch> and UEFI firmware for it. Output, including
# serial.log, goes to _out/variants/.
#
# The support and variant images are built by hack/e2e-variants/buildimg
# (see that command's doc comment for why: it lets this script set
# manifest-level annotations the same way regardless of which container
# engine built the Alpine example, rather than relying on
# `podman build --annotation`/`docker buildx --annotation`, which differ
# enough between engines and versions to be a poor fit here).
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO}/_out/variants"
TIMEOUT="180s"
MARKER="contemper-variant-openrc"
BOOT_MARKER="contemper-boot-ok"
IMAGE="contemper-example:dev"
# Not 5000: macOS AirPlay Receiver listens there.
REGISTRY_HOST="${E2E_REGISTRY:-localhost:5555}"
REGISTRY_NAME="contemper-e2e-registry"
REPO_PREFIX="${REGISTRY_HOST}/contemper-e2e"
TAG="e2e"

while [ $# -gt 0 ]; do
	case "$1" in
	--timeout)
		TIMEOUT="$2"
		shift 2
		;;
	*)
		echo "e2e-variants.sh: unknown argument: $1" >&2
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
	echo "e2e-variants.sh: neither podman nor docker found; set CONTAINER_ENGINE" >&2
	exit 1
fi
command -v go >/dev/null || { echo "e2e-variants.sh: go not found" >&2; exit 1; }
command -v curl >/dev/null || { echo "e2e-variants.sh: curl not found" >&2; exit 1; }

# Build per OS/arch, same convention as hack/e2e.sh.
platform="$(go env GOOS)-$(go env GOARCH)"
arch="$(go env GOARCH)"
contemper="${REPO}/bin/${platform}/contemper"
echo "==> go build (${platform})" >&2
(cd "${REPO}" && go build -o "bin/${platform}/contemper" ./cmd/contemper)

rm -rf "${OUT}"
mkdir -p "${OUT}"

buildimg="${OUT}/buildimg"
echo "==> go build hack/e2e-variants/buildimg" >&2
(cd "${REPO}" && go build -o "${buildimg}" ./hack/e2e-variants/buildimg)

# --- local registry -------------------------------------------------------
#
# go-containerregistry (which contemper uses to talk to registries)
# treats any "localhost:<port>" reference as plain HTTP automatically
# (see name.Registry.Scheme in the vendored library), so no extra
# insecure-registry configuration is needed on either contemper's side or
# this script's: pushing to localhost:<port> and pulling it back with
# `contemper convert --support` both just work.
registry_started=0
registry_up() { curl -fsS -o /dev/null "http://${REGISTRY_HOST}/v2/"; }

if ! registry_up; then
	echo "==> starting a local registry (${engine} run registry:2)" >&2
	"${engine}" rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	"${engine}" run -d --rm --name "${REGISTRY_NAME}" -p "127.0.0.1:${REGISTRY_HOST##*:}:5000" registry:2 >/dev/null
	registry_started=1
	for _ in $(seq 1 50); do
		if registry_up; then
			break
		fi
		sleep 0.2
	done
	if ! registry_up; then
		echo "e2e-variants.sh: local registry did not become ready" >&2
		exit 1
	fi
fi
cleanup_registry() {
	if [ "${registry_started}" -eq 1 ]; then
		"${engine}" stop "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	fi
}
trap cleanup_registry EXIT

# --- Alpine example image, as a local archive source (same as hack/e2e.sh) -
echo "==> ${engine} build examples/alpine" >&2
"${engine}" build -t "${IMAGE}" -f "${REPO}/examples/alpine/Containerfile" "${REPO}/examples/alpine"

case "${engine}" in
podman)
	"${engine}" save --format oci-archive -o "${OUT}/example.tar" "${IMAGE}"
	source_ref="oci-archive:${OUT}/example.tar"
	;;
*)
	"${engine}" save -o "${OUT}/example.tar" "${IMAGE}"
	source_ref="docker-archive:${OUT}/example.tar"
	;;
esac

# --- dummy support image + one variant, pushed to the local registry ------
#
# Branch "init-system": variant "openrc" matches (the Alpine example has
# /sbin/openrc) and contributes a local.d script; variant "systemd"
# never matches (no image on this appliance ever has
# /usr/lib/systemd/systemd) and points at an image that is never pushed,
# to double as a check that a losing variant's manifest is never
# fetched (the docs' resolution algorithm, step 3).
#
# Branch "extras": variant "custom" never matches (its path never
# exists) and, like "systemd" above, is never pushed; "none" is the
# branch's declared default and is a no-op (no .image), exercising the
# default path.
support_ref="${REPO_PREFIX}/support:${TAG}"
openrc_ref="${REPO_PREFIX}/support-openrc:${TAG}"
never_pushed_systemd_ref="${REPO_PREFIX}/support-systemd:not-pushed"
never_pushed_custom_ref="${REPO_PREFIX}/support-custom:not-pushed"

echo "==> pushing support-openrc variant image" >&2
"${buildimg}" -ref "${openrc_ref}" -arch "${arch}" \
	-exec "/etc/local.d/zzz-contemper-variant-openrc.start=${REPO}/hack/e2e-variants/files/openrc-local.start"

echo "==> pushing dummy support image" >&2
"${buildimg}" -ref "${support_ref}" -arch "${arch}" \
	-file "/etc/contemper-support-marker=${REPO}/hack/e2e-variants/files/support-marker" \
	-annotation "io.contemper.branch.init-system.openrc.requires.files=/sbin/openrc" \
	-annotation "io.contemper.branch.init-system.openrc.image=${openrc_ref}" \
	-annotation "io.contemper.branch.init-system.systemd.requires.files=/usr/lib/systemd/systemd" \
	-annotation "io.contemper.branch.init-system.systemd.image=${never_pushed_systemd_ref}" \
	-annotation "io.contemper.branch.extras.custom.requires.files=/etc/contemper-e2e-extras-marker" \
	-annotation "io.contemper.branch.extras.custom.image=${never_pushed_custom_ref}" \
	-annotation "io.contemper.branch.extras.default=none"

# --- convert, boot ---------------------------------------------------------
echo "==> contemper convert --support ${support_ref}" >&2
bundle="$("${contemper}" convert --target qemu --support "${support_ref}" "${source_ref}" -o "${OUT}")"

manifest="${bundle}/contemper.json"
echo "==> checking resolved variants in ${manifest}" >&2
if ! grep -A2 '"branch": "init-system"' "${manifest}" | grep -q '"variant": "openrc"'; then
	echo "e2e-variants.sh: contemper.json does not record branch init-system -> openrc:" >&2
	cat "${manifest}" >&2
	exit 1
fi
if ! grep -A2 '"branch": "extras"' "${manifest}" | grep -q '"variant": "none"'; then
	echo "e2e-variants.sh: contemper.json does not record branch extras -> none:" >&2
	cat "${manifest}" >&2
	exit 1
fi
echo "==> contemper.json OK: init-system -> openrc, extras -> none (default)" >&2

echo "==> contemper deploy --to local-qemu" >&2
"${contemper}" deploy --to local-qemu "${bundle}" \
	--expect "${MARKER}" \
	--timeout "${TIMEOUT}" \
	--serial-log "${OUT}/serial.log"

if ! grep -q "${BOOT_MARKER}" "${OUT}/serial.log"; then
	echo "e2e-variants.sh: ${BOOT_MARKER} (the example's own marker) never appeared on the serial console" >&2
	exit 1
fi
echo "==> boot OK: both ${BOOT_MARKER} and ${MARKER} seen on the serial console" >&2

# --- negative cases: convert must fail, without booting anything ----------
#
# assert_convert_fails builds a support image from the given annotations,
# runs `contemper convert` against it, and checks it fails naming
# every expected substring (the branch name, plus whatever else pins the
# failure down) in its combined output.
assert_convert_fails() {
	local name="$1" support_ref="$2"
	shift 2
	local out status
	set +e
	out="$("${contemper}" convert --target qemu --support "${support_ref}" "${source_ref}" -o "${OUT}/neg-${name}" 2>&1)"
	status=$?
	set -e
	if [ "${status}" -eq 0 ]; then
		echo "e2e-variants.sh: expected convert to fail for the ${name} support image, but it exited 0:" >&2
		echo "${out}" >&2
		exit 1
	fi
	local want
	for want in "$@"; do
		if ! grep -qF -- "${want}" <<<"${out}"; then
			echo "e2e-variants.sh: expected the ${name} failure to mention '${want}'; got:" >&2
			echo "${out}" >&2
			exit 1
		fi
	done
	echo "==> ${name} OK: convert failed (exit ${status}) naming ${*}" >&2
}

echo "==> pushing ambiguous-branch support image (negative case)" >&2
ambiguous_ref="${REPO_PREFIX}/support-ambiguous:${TAG}"
"${buildimg}" -ref "${ambiguous_ref}" -arch "${arch}" \
	-annotation "io.contemper.branch.init-system.openrc.requires.files=/sbin/openrc" \
	-annotation "io.contemper.branch.init-system.always.requires.files=/etc/os-release"
assert_convert_fails "ambiguous" "${ambiguous_ref}" \
	"branch init-system" "openrc" "always"

echo "==> pushing no-match-no-default support image (negative case)" >&2
missing_ref="${REPO_PREFIX}/support-missing:${TAG}"
"${buildimg}" -ref "${missing_ref}" -arch "${arch}" \
	-annotation "io.contemper.branch.init-system.systemd.requires.files=/usr/lib/systemd/systemd"
assert_convert_fails "missing" "${missing_ref}" \
	"branch init-system" "no default is declared" "/usr/lib/systemd/systemd"

echo "==> e2e-variants OK" >&2
