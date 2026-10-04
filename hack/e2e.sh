#!/usr/bin/env bash
# End-to-end boot test: build the chosen example image (--example
# alpine|debian|debian-grub|archlinux, default alpine) for the host
# architecture, convert it with `contemper convert --target qemu`, and
# boot the bundle with `contemper deploy --to local-qemu`, passing once
# the image's boot marker appears on the serial console.
#
# With --distro ID instead, it runs one entry of the distribution matrix
# (test/distros/matrix.json): the family's test image is built from
# test/distros/<family>/Containerfile with the entry's base image and
# build args, converted, booted, and must print the in-guest check's
# boot marker. A failed in-guest check prints a "contemper-check-failed:"
# line on the serial console, which stops the wait right away.
#
# Usage: hack/e2e.sh [--example alpine|debian|debian-grub|archlinux]
#                     [--timeout DURATION] [--build-command]
#        hack/e2e.sh --distro ID [--registry-mirror HOST] [--timeout DURATION]
#                    [--status-dir DIR]
#
# --registry-mirror HOST (or CONTEMPER_E2E_REGISTRY_MIRROR=HOST) pulls
# Docker Hub base images through a mirror such as mirror.gcr.io, which
# avoids Docker Hub's anonymous pull rate limit when many entries run
# from one address. Only Docker Hub references are rewritten (alpine:3.24
# becomes HOST/library/alpine:3.24, rockylinux/rockylinux:9 becomes
# HOST/rockylinux/rockylinux:9); quay.io, registry.opensuse.org and other
# registries are left alone.
#
# --status-dir DIR (with --distro) records how far the run got, for
# reporting: DIR/stage holds the stage being run (build, convert or boot,
# then done once the marker was seen), so after a failure it names the
# stage that failed, and DIR/base-digest the resolved digest of the base
# image when the engine can tell. DIR is created and not cleaned.
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
DISTRO=""
MIRROR="${CONTEMPER_E2E_REGISTRY_MIRROR:-}"
STATUS_DIR=""
FAIL_MARKER="contemper-check-failed:"

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
	--distro)
		DISTRO="$2"
		shift 2
		;;
	--registry-mirror)
		MIRROR="$2"
		shift 2
		;;
	--status-dir)
		STATUS_DIR="$2"
		shift 2
		;;
	*)
		echo "e2e.sh: unknown argument: $1" >&2
		exit 2
		;;
	esac
done

if [ -n "${MIRROR}" ] && [ -z "${DISTRO}" ]; then
	echo "e2e.sh: --registry-mirror requires --distro" >&2
	exit 2
fi
if [ -n "${STATUS_DIR}" ] && [ -z "${DISTRO}" ]; then
	echo "e2e.sh: --status-dir requires --distro" >&2
	exit 2
fi
if [ -n "${DISTRO}" ] && [ "${BUILD_COMMAND}" -eq 1 ]; then
	echo "e2e.sh: --distro and --build-command can't be combined" >&2
	exit 2
fi

DISTRO_BUILD_ARGS=()
if [ -n "${STATUS_DIR}" ]; then
	mkdir -p "${STATUS_DIR}"
	rm -f "${STATUS_DIR}/stage" "${STATUS_DIR}/base-digest"
fi
# set_stage NAME records the stage being run (see --status-dir).
set_stage() {
	[ -z "${STATUS_DIR}" ] || echo "$1" >"${STATUS_DIR}/stage"
}
set_stage build
if [ -n "${DISTRO}" ]; then
	# The matrix entry is read by a small Go helper (Go is needed for the
	# build below anyway, so there is no jq dependency on macOS hosts).
	command -v go >/dev/null || { echo "e2e.sh: go not found" >&2; exit 1; }
	entry="$(cd "${REPO}" && go run ./hack/distro-entry "${DISTRO}" ${MIRROR:+"${MIRROR}"})" || exit 2
	distro_base=""
	distro_dir=""
	distro_arches=""
	while IFS= read -r line; do
		case "${line}" in
		base=*) distro_base="${line#base=}" ;;
		containerfile=*) distro_dir="${line#containerfile=}" ;;
		arch=*) distro_arches="${line#arch=}" ;;
		buildarg=*) DISTRO_BUILD_ARGS+=(--build-arg "${line#buildarg=}") ;;
		esac
	done <<<"${entry}"
	case "$(uname -m)" in
	x86_64 | amd64) host_arch=amd64 ;;
	arm64 | aarch64) host_arch=arm64 ;;
	*) host_arch="$(uname -m)" ;;
	esac
	case " ${distro_arches} " in
	*" ${host_arch} "*) ;;
	*)
		echo "e2e.sh: --distro ${DISTRO} is only listed for: ${distro_arches} (this host is ${host_arch})" >&2
		exit 2
		;;
	esac
	IMAGE="contemper-distro-${DISTRO}:dev"
	LABEL="${DISTRO}"
else
	case "${EXAMPLE}" in
	alpine | debian | debian-grub | archlinux) ;;
	*)
		echo "e2e.sh: unknown --example '${EXAMPLE}' (want alpine, debian, debian-grub or archlinux)" >&2
		exit 2
		;;
	esac
	LABEL="examples/${EXAMPLE}"
fi

# The archlinux example's base image is only published for amd64; catch
# that here, before spending time on a container build that would only
# fail inside the Containerfile itself.
if [ -z "${DISTRO}" ] && [ "${EXAMPLE}" = "archlinux" ] && [ "$(uname -m)" != "x86_64" ]; then
	echo "e2e.sh: --example archlinux requires an amd64 host (got $(uname -m)); the archlinux base image isn't published for other architectures" >&2
	exit 2
fi
[ -n "${DISTRO}" ] || IMAGE="contemper-example-${EXAMPLE}:dev"

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
	echo "==> ${engine} build ${LABEL}" >&2
	if [ -n "${DISTRO}" ]; then
		# The context is test/distros, so the Containerfiles can COPY from common/.
		"${engine}" build -t "${IMAGE}" \
			--build-arg "BASE=${distro_base}" ${DISTRO_BUILD_ARGS[@]+"${DISTRO_BUILD_ARGS[@]}"} \
			-f "${REPO}/test/distros/${distro_dir}/Containerfile" "${REPO}/test/distros"
	else
		"${engine}" build -t "${IMAGE}" -f "${REPO}/examples/${EXAMPLE}/Containerfile" "${REPO}/examples/${EXAMPLE}"
	fi

	if [ -n "${STATUS_DIR}" ] && [ -n "${DISTRO}" ]; then
		# The build pulled the base image, so the engine knows its digest.
		"${engine}" image inspect --format '{{index .RepoDigests 0}}' "${distro_base}" \
			>"${STATUS_DIR}/base-digest" 2>/dev/null || rm -f "${STATUS_DIR}/base-digest"
	fi

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

	set_stage convert
	echo "==> contemper convert" >&2
	bundle="$("${contemper}" convert --target qemu "${source_ref}" -o "${OUT}")"
fi

set_stage boot
echo "==> contemper deploy --to local-qemu" >&2
# Run the deploy in the background and watch the serial log, so a failed
# in-guest check ends the run at once instead of at the timeout.
"${contemper}" deploy --to local-qemu "${bundle}" \
	--expect "${MARKER}" \
	--timeout "${TIMEOUT}" \
	--serial-log "${OUT}/serial.log" &
deploy_pid=$!
check_failed=0
while kill -0 "${deploy_pid}" 2>/dev/null; do
	if grep -q "${FAIL_MARKER}" "${OUT}/serial.log" 2>/dev/null; then
		check_failed=1
		kill -TERM "${deploy_pid}" 2>/dev/null || true
		break
	fi
	sleep 1
done
deploy_rc=0
wait "${deploy_pid}" || deploy_rc=$?
if [ "${check_failed}" -eq 1 ]; then
	echo "e2e.sh: in-guest check failed: $(grep -h "${FAIL_MARKER}" "${OUT}/serial.log" | head -n 1)" >&2
	exit 1
fi
[ "${deploy_rc}" -eq 0 ] || exit "${deploy_rc}"

set_stage "done"
echo "==> e2e OK: ${MARKER} seen on the serial console" >&2
