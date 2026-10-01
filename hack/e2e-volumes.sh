#!/usr/bin/env bash
# End-to-end volumes test: build the chosen example image (--example
# alpine|debian, default alpine), derive a second image from it
# (hack/e2e-volumes/Containerfile) that declares two volumes and checks
# them on boot, build and push the volume-formatting helper images to a
# local registry with hack/volumes-support/buildimg, convert with
# `contemper convert --volume-helper <local ref>`, then:
#
#  1. deploy with a fixed --name into a temp XDG_STATE_HOME: expect
#     contemper-volume-fresh (the /data volume was blank, formatted,
#     mounted) and contemper-volume-seeded (the image's own content at
#     /data was copied onto it, with its original owner and mode).
#  2. deploy the same bundle again, same --name: expect
#     contemper-volume-persisted (the previous boot's marker survived)
#     and contemper-volume-modified-persisted (a change the first boot
#     made to the seeded file also survived - the reused disk was never
#     reformatted or re-seeded over it).
#  3. build a second version of the derived image (a changed file, a new
#     tag), convert it, and deploy it *without* --name: expect
#     contemper-volume-persisted and contemper-volume-modified-persisted
#     again, since the default instance name (the source image's
#     repository, not its tag) is the same as before, so it's the same
#     volume disk.
#
# /data-noseed, declared with io.contemper.volume./data-noseed.seed="false",
# is checked on every boot above: it must stay mounted but empty -
# contemper-volume-noseed-ok - never picking up the image's own content
# at that path the way /data does.
#
# Every deploy also checks contemper-boot-ok (the example's own marker)
# and contemper-volume-mounted (the label the helper gave each disk)
# appear on the console, and fails if any contemper-volume-*-missing,
# *-mismatch or *-leaked marker (see hack/e2e-volumes/files/check-volume.sh)
# appears at all.
#
# Usage: hack/e2e-volumes.sh [--example alpine|debian] [--timeout DURATION]
#
# Requires: go, podman or docker (CONTAINER_ENGINE selects one
# explicitly), curl, e2fsprogs (mkfs.ext4, debugfs, e2fsck),
# qemu-img, qemu-system-<arch> and UEFI firmware for it. Output,
# including each deploy's serial log, goes to _out/volumes/.
#
# Set CONTEMPER_E2E_COVERDIR to a directory to build contemper with Go's
# source-code coverage instrumentation and collect the integration
# coverage from every invocation below into it (see `go help testflag`'s
# GOCOVERDIR, and `go tool covdata`). The volumes-support buildimg helper
# is never instrumented. Unset, nothing here changes.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO}/_out/volumes"
TIMEOUT="180s"
BOOT_MARKER="contemper-boot-ok"
# A trailing space (before "dev=") keeps this from also matching the
# "label=data-noseed" line below: that label starts with "data" too, but
# is never followed by a space at that point.
MOUNTED_MARKER="contemper-volume-mounted label=data dev="
MOUNTED_MARKER_NOSEED="contemper-volume-mounted label=data-noseed dev="
NOSEED_OK_MARKER="contemper-volume-noseed-ok"
SEEDED_MARKER="contemper-volume-seeded"
MODIFIED_PERSISTED_MARKER="contemper-volume-modified-persisted"
# None of these must ever appear on any boot - see
# hack/e2e-volumes/files/check-volume.sh for what would print each one.
FAILURE_MARKERS=(
	"contemper-volume-not-mounted"
	"contemper-volume-seed-missing"
	"contemper-volume-seed-mismatch"
	"contemper-volume-modified-missing"
	"contemper-volume-noseed-leaked"
)
EXAMPLE="alpine"
# Not 5000: macOS AirPlay Receiver listens there.
REGISTRY_HOST="${E2E_REGISTRY:-localhost:5555}"
REGISTRY_NAME="contemper-e2e-registry"
REPO_PREFIX="${REGISTRY_HOST}/contemper-e2e"
TAG="e2e"

while [ $# -gt 0 ]; do
	case "$1" in
	--example)
		EXAMPLE="$2"
		shift 2
		;;
	--timeout)
		TIMEOUT="$2"
		shift 2
		;;
	*)
		echo "e2e-volumes.sh: unknown argument: $1" >&2
		exit 2
		;;
	esac
done

case "${EXAMPLE}" in
alpine | debian) ;;
*)
	echo "e2e-volumes.sh: unknown --example '${EXAMPLE}' (want alpine or debian)" >&2
	exit 2
	;;
esac
IMAGE="contemper-example-${EXAMPLE}:dev"
DERIVED_IMAGE="contemper-example-volumes-${EXAMPLE}:e2e"
DERIVED_IMAGE_V2="contemper-example-volumes-${EXAMPLE}:e2e-v2"

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
	echo "e2e-volumes.sh: neither podman nor docker found; set CONTAINER_ENGINE" >&2
	exit 1
fi
command -v go >/dev/null || { echo "e2e-volumes.sh: go not found" >&2; exit 1; }
command -v curl >/dev/null || { echo "e2e-volumes.sh: curl not found" >&2; exit 1; }

COVERDIR="${CONTEMPER_E2E_COVERDIR:-}"
if [ -n "${COVERDIR}" ]; then
	mkdir -p "${COVERDIR}"
	# Absolute, since Go resolves a relative GOCOVERDIR against each
	# process's own working directory.
	COVERDIR="$(cd "${COVERDIR}" && pwd)"
	export GOCOVERDIR="${COVERDIR}"
fi

platform="$(go env GOOS)-$(go env GOARCH)"
arch="$(go env GOARCH)"
contemper="${REPO}/bin/${platform}/contemper"
echo "==> go build (${platform})" >&2
if [ -n "${COVERDIR}" ]; then
	(cd "${REPO}" && go build -cover -coverpkg=./... -o "bin/${platform}/contemper" ./cmd/contemper)
else
	(cd "${REPO}" && go build -o "bin/${platform}/contemper" ./cmd/contemper)
fi

rm -rf "${OUT}"
mkdir -p "${OUT}"

buildimg="${OUT}/volumes-support-buildimg"
echo "==> go build hack/volumes-support/buildimg" >&2
(cd "${REPO}" && go build -o "${buildimg}" ./hack/volumes-support/buildimg)

# --- local registry -------------------------------------------------------
registry_started=0
registry_up() { curl -fsS -o /dev/null "http://${REGISTRY_HOST}/v2/"; }

if ! registry_up; then
	echo "==> starting a local registry (${engine} run registry:3)" >&2
	"${engine}" rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	"${engine}" run -d --rm --name "${REGISTRY_NAME}" -p "127.0.0.1:${REGISTRY_HOST##*:}:5000" registry:3 >/dev/null
	registry_started=1
	for _ in $(seq 1 50); do
		if registry_up; then
			break
		fi
		sleep 0.2
	done
	if ! registry_up; then
		echo "e2e-volumes.sh: local registry did not become ready" >&2
		exit 1
	fi
fi
cleanup_registry() {
	if [ "${registry_started}" -eq 1 ]; then
		"${engine}" stop "${REGISTRY_NAME}" >/dev/null 2>&1 || true
	fi
}
trap cleanup_registry EXIT

# --- example image, plus a derived image declaring one volume -------------
echo "==> ${engine} build examples/${EXAMPLE}" >&2
"${engine}" build -t "${IMAGE}" -f "${REPO}/examples/${EXAMPLE}/Containerfile" "${REPO}/examples/${EXAMPLE}"

echo "==> ${engine} build hack/e2e-volumes (revision 1, from ${IMAGE})" >&2
"${engine}" build -t "${DERIVED_IMAGE}" --build-arg BASE_IMAGE="${IMAGE}" --build-arg REVISION=1 -f "${REPO}/hack/e2e-volumes/Containerfile" "${REPO}/hack/e2e-volumes"

save_archive() {
	local image="$1" out_tar="$2"
	case "${engine}" in
	podman)
		"${engine}" save --format oci-archive -o "${out_tar}" "${image}"
		echo "oci-archive:${out_tar}"
		;;
	*)
		"${engine}" save -o "${out_tar}" "${image}"
		echo "docker-archive:${out_tar}"
		;;
	esac
}

source_ref="$(save_archive "${DERIVED_IMAGE}" "${OUT}/example-volumes.tar")"

# --- volume-formatting helper, pushed to the local registry ---------------
echo "==> building and pushing the volumes-support helper images" >&2
"${buildimg}" -prefix "${REPO_PREFIX}" -tag "${TAG}" -support-dir "${REPO}/support/volumes-support"
helper_ref="${REPO_PREFIX}/volumes-support:${TAG}"

# --- convert, deploy fresh then persisted ----------------------------------
echo "==> contemper convert --volume-helper ${helper_ref}" >&2
bundle="$("${contemper}" convert --target qemu --arch "${arch}" --volume-helper "${helper_ref}" "${source_ref}" -o "${OUT}")"

export XDG_STATE_HOME="${OUT}/xdgstate"

# deploy_and_check runs one deploy and checks its serial log: $1 label,
# $2 bundle dir, $3 --expect string, $4 log path, $5 a space-separated
# list of additional markers that must also appear (on top of
# BOOT_MARKER, MOUNTED_MARKER and MOUNTED_MARKER_NOSEED, required on
# every call), and every remaining argument is passed straight through
# to `contemper deploy`. It also fails if any FAILURE_MARKERS string
# appears at all, on any call.
deploy_and_check() {
	local label="$1" bundle_dir="$2" expect="$3" log="$4" extra_markers="$5"
	shift 5
	echo "==> contemper deploy --to local-qemu (${label})" >&2
	"${contemper}" deploy --to local-qemu "${bundle_dir}" \
		--expect "${expect}" \
		--timeout "${TIMEOUT}" \
		--serial-log "${log}" \
		"$@"

	local marker
	for marker in "${BOOT_MARKER}" "${MOUNTED_MARKER}" "${MOUNTED_MARKER_NOSEED}" ${extra_markers}; do
		if ! grep -q "${marker}" "${log}"; then
			echo "e2e-volumes.sh: ${label}: expected \"${marker}\" on the console; log:" >&2
			cat "${log}" >&2
			exit 1
		fi
	done
	for marker in "${FAILURE_MARKERS[@]}"; do
		if grep -q "${marker}" "${log}"; then
			echo "e2e-volumes.sh: ${label}: unexpected \"${marker}\" on the console; log:" >&2
			cat "${log}" >&2
			exit 1
		fi
	done
	echo "==> ${label} OK: ${expect} and every expected marker seen, no failure marker" >&2
}

# No --name in any of these three deploys: every one relies on the
# default instance name (the source image's repository, the same for
# both revisions built below), which is exactly what makes the third
# deploy - a different tag, converted separately - land on the same
# volume disk as the first two.
deploy_and_check "first boot" "${bundle}" "contemper-volume-fresh" "${OUT}/serial-fresh.log" \
	"${NOSEED_OK_MARKER} ${SEEDED_MARKER}"
deploy_and_check "second boot, same bundle" "${bundle}" "contemper-volume-persisted" "${OUT}/serial-persisted.log" \
	"${NOSEED_OK_MARKER} ${MODIFIED_PERSISTED_MARKER}"

# --- an image update: same instance (source repo, no tag), new content ----
echo "==> ${engine} build hack/e2e-volumes (revision 2, from ${IMAGE})" >&2
"${engine}" build -t "${DERIVED_IMAGE_V2}" --build-arg BASE_IMAGE="${IMAGE}" --build-arg REVISION=2 -f "${REPO}/hack/e2e-volumes/Containerfile" "${REPO}/hack/e2e-volumes"
source_ref_v2="$(save_archive "${DERIVED_IMAGE_V2}" "${OUT}/example-volumes-v2.tar")"

echo "==> contemper convert (revision 2)" >&2
bundle_v2="$("${contemper}" convert --target qemu --arch "${arch}" --volume-helper "${helper_ref}" "${source_ref_v2}" -o "${OUT}")"

# No --name here: the default instance name is the source image's
# repository (the same for both revisions, since only the tag changed),
# so this reuses the first deploy's volume disk without being told to.
deploy_and_check "image update, default instance" "${bundle_v2}" "contemper-volume-persisted" "${OUT}/serial-update.log" \
	"${NOSEED_OK_MARKER} ${MODIFIED_PERSISTED_MARKER}"

echo "==> e2e-volumes OK" >&2
