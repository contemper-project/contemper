#!/usr/bin/env bash
# End-to-end boot test: build the chosen example image (--example
# alpine|debian|archlinux, default alpine) for the host architecture,
# convert it with `contemper convert --target qemu`, and boot the bundle
# with `contemper deploy --to local-qemu`, passing once the image's boot
# marker appears on the serial console.
#
# Usage: hack/e2e.sh [--example alpine|debian|archlinux] [--timeout DURATION]
#                     [--build-command]
#
# Requires: go, podman or docker (CONTAINER_ENGINE selects one explicitly),
# e2fsprogs (mkfs.ext4, debugfs, e2fsck), qemu-img, qemu-system-<arch> and
# UEFI firmware for it. Output, including serial.log, goes to _out/.
#
# --build-command builds and converts the example through `contemper
# build` instead of the engine build/save + `contemper convert` steps
# below, exercising the build command and its docker-daemon: source
# against a real Docker daemon. It requires docker with the buildx
# plugin on PATH (podman/docker/CONTAINER_ENGINE selection is skipped);
# missing either fails fast, before the go build below.
#
# Set CONTEMPER_E2E_COVERDIR to a directory to build contemper with Go's
# source-code coverage instrumentation and collect the integration
# coverage from every invocation below into it (see `go help testflag`'s
# GOCOVERDIR, and `go tool covdata`). Unset, nothing here changes.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO}/_out"
TIMEOUT="180s"
MARKER="contemper-boot-ok"
EXAMPLE="alpine"
BUILD_COMMAND=0

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
	--build-command)
		BUILD_COMMAND=1
		shift
		;;
	*)
		echo "e2e.sh: unknown argument: $1" >&2
		exit 2
		;;
	esac
done

case "${EXAMPLE}" in
alpine | debian | archlinux) ;;
*)
	echo "e2e.sh: unknown --example '${EXAMPLE}' (want alpine, debian or archlinux)" >&2
	exit 2
	;;
esac

# The archlinux example's base image is only published for amd64; catch
# that here, before spending time on a container build that would only
# fail inside the Containerfile itself.
if [ "${EXAMPLE}" = "archlinux" ] && [ "$(uname -m)" != "x86_64" ]; then
	echo "e2e.sh: --example archlinux requires an amd64 host (got $(uname -m)); the archlinux base image isn't published for other architectures" >&2
	exit 2
fi
IMAGE="contemper-example-${EXAMPLE}:dev"

if [ "${BUILD_COMMAND}" -eq 1 ]; then
	# `contemper build` itself shells out to docker buildx; check here too,
	# so a missing docker/buildx fails before the go build below rather
	# than after.
	command -v docker >/dev/null || { echo "e2e.sh: --build-command requires docker on PATH" >&2; exit 1; }
	docker buildx version >/dev/null 2>&1 || { echo "e2e.sh: --build-command requires docker buildx ('docker buildx version' failed)" >&2; exit 1; }
else
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
		echo "e2e.sh: neither podman nor docker found; set CONTAINER_ENGINE" >&2
		exit 1
	fi
fi
command -v go >/dev/null || { echo "e2e.sh: go not found" >&2; exit 1; }

COVERDIR="${CONTEMPER_E2E_COVERDIR:-}"
if [ -n "${COVERDIR}" ]; then
	mkdir -p "${COVERDIR}"
	# Absolute, since Go resolves a relative GOCOVERDIR against each
	# process's own working directory.
	COVERDIR="$(cd "${COVERDIR}" && pwd)"
	export GOCOVERDIR="${COVERDIR}"
fi

# Build per OS/arch so a checkout shared between machines (e.g. a Linux
# container and a macOS host) never runs the other platform's binary.
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

if [ "${BUILD_COMMAND}" -eq 1 ]; then
	echo "==> contemper build examples/${EXAMPLE}" >&2
	bundle="$("${contemper}" build --target qemu -o "${OUT}" -t "${IMAGE}" "${REPO}/examples/${EXAMPLE}")"
else
	echo "==> ${engine} build examples/${EXAMPLE}" >&2
	"${engine}" build -t "${IMAGE}" -f "${REPO}/examples/${EXAMPLE}/Containerfile" "${REPO}/examples/${EXAMPLE}"

	# podman can write an OCI archive; docker save only writes its own format.
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

	echo "==> contemper convert" >&2
	bundle="$("${contemper}" convert --target qemu "${source_ref}" -o "${OUT}")"
fi

echo "==> contemper deploy --to local-qemu" >&2
"${contemper}" deploy --to local-qemu "${bundle}" \
	--expect "${MARKER}" \
	--timeout "${TIMEOUT}" \
	--serial-log "${OUT}/serial.log"

echo "==> e2e OK: ${MARKER} seen on the serial console" >&2
