#!/usr/bin/env bash
# End-to-end multi-architecture test: build examples/alpine for
# linux/amd64 and linux/arm64 into one multi-platform OCI archive, convert
# it with `contemper convert --target qemu --arch all`, check the bundles
# and the <name>.multiarch.json group file, then boot the group twice with
# `contemper deploy --to local-qemu`: once for the host architecture and
# once with --arch for the other one, each passing once the image's boot
# marker appears on the serial console. The foreign architecture runs
# under QEMU's software emulation (TCG), so it is slow.
#
# Usage: hack/e2e-multiarch.sh [--timeout DURATION]
#
# --timeout applies to the boot of the host architecture; the foreign
# architecture gets a fixed 600s.
#
# Requires: go, docker with the buildx plugin (CONTAINER_ENGINE, if set,
# must be docker: podman can't produce a multi-platform archive this
# way), QEMU user-mode emulation registered for the foreign architecture
# (binfmt_misc, so the build's RUN steps can execute), e2fsprogs
# (mkfs.ext4, debugfs, e2fsck), qemu-img, qemu-system-x86_64 and
# qemu-system-aarch64, and UEFI firmware for both. Output goes to
# _out/multiarch/ (serial-<arch>.log are the serial logs).
#
# The build uses a throwaway buildx builder with the docker-container
# driver, since the default driver can't export multi-platform images;
# it is removed on exit.
#
# Set CONTEMPER_E2E_COVERDIR to a directory to build contemper with Go's
# source-code coverage instrumentation and collect the integration
# coverage from every invocation below into it (see `go help testflag`'s
# GOCOVERDIR, and `go tool covdata`). Unset, nothing here changes.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${REPO}/_out/multiarch"
TIMEOUT="300s"
FOREIGN_TIMEOUT="600s"
MARKER="contemper-boot-ok"
BUILDER="contemper-e2e-multiarch"

while [ $# -gt 0 ]; do
	case "$1" in
	--timeout)
		TIMEOUT="$2"
		shift 2
		;;
	*)
		echo "e2e-multiarch.sh: unknown argument: $1" >&2
		exit 2
		;;
	esac
done

fail() {
	echo "e2e-multiarch.sh: $*" >&2
	exit 1
}

engine="${CONTAINER_ENGINE:-docker}"
[ "${engine}" = "docker" ] || fail "CONTAINER_ENGINE=${engine}: a multi-platform build needs docker with buildx"
command -v docker >/dev/null || fail "docker not found"
docker buildx version >/dev/null 2>&1 || fail "docker buildx is required ('docker buildx version' failed)"
command -v go >/dev/null || fail "go not found"

case "$(uname -m)" in
x86_64 | amd64)
	host_arch=amd64
	foreign_arch=arm64
	;;
aarch64 | arm64)
	host_arch=arm64
	foreign_arch=amd64
	;;
*) fail "unsupported host architecture $(uname -m)" ;;
esac

# A foreign-platform RUN step needs binfmt_misc, which is host kernel
# state; catching it here beats a build failing with "exec format error".
if [ "$(uname -s)" = "Linux" ]; then
	found=0
	for f in /proc/sys/fs/binfmt_misc/qemu-*; do
		case "${f}" in
		*"${foreign_arch/amd64/x86_64}"* | *"${foreign_arch/arm64/aarch64}"*)
			[ -e "${f}" ] && found=1
			;;
		esac
	done
	[ "${found}" -eq 1 ] || fail "no QEMU binfmt handler registered for ${foreign_arch} (try: docker run --privileged --rm tonistiigi/binfmt --install ${foreign_arch})"
fi

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

docker buildx rm -f "${BUILDER}" >/dev/null 2>&1 || true
cleanup() { docker buildx rm -f "${BUILDER}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> docker buildx build examples/alpine (linux/amd64,linux/arm64)" >&2
docker buildx create --name "${BUILDER}" --driver docker-container >/dev/null
docker buildx build --builder "${BUILDER}" \
	--platform linux/amd64,linux/arm64 \
	--output "type=oci,dest=${OUT}/example.tar" \
	-f "${REPO}/examples/alpine/Containerfile" "${REPO}/examples/alpine" ||
	fail "multi-platform build failed (is QEMU binfmt registered for both platforms?)"

echo "==> contemper convert --arch all" >&2
bundles="$("${contemper}" convert --target qemu --arch all "oci-archive:${OUT}/example.tar" -o "${OUT}")"
echo "${bundles}"

n="$(printf '%s\n' "${bundles}" | grep -c .)"
[ "${n}" -eq 2 ] || fail "expected 2 bundle paths on stdout, got ${n}"
while IFS= read -r b; do
	[ -d "${b}" ] || fail "bundle directory ${b} does not exist"
done <<<"${bundles}"

group=""
for f in "${OUT}"/*.multiarch.json; do
	[ -e "${f}" ] || fail "no .multiarch.json group file in ${OUT}"
	[ -z "${group}" ] || fail "more than one group file in ${OUT}"
	group="${f}"
done
for arch in amd64 arm64; do
	if command -v jq >/dev/null; then
		jq -e --arg a "${arch}" '.bundles | map(.arch) | index($a) != null' "${group}" >/dev/null ||
			fail "${group} does not list ${arch}"
	else
		grep -q "\"arch\": *\"${arch}\"" "${group}" || fail "${group} does not list ${arch}"
	fi
done

boot() {
	local arch="$1" timeout="$2" report
	shift 2
	echo "==> contemper deploy --to local-qemu (${arch})" >&2
	# The progress report goes to stderr; keep it visible and capture it
	# (pipefail makes a failed deploy fail the pipeline).
	"${contemper}" deploy --to local-qemu "$@" "${group}" \
		--expect "${MARKER}" \
		--timeout "${timeout}" \
		--serial-log "${OUT}/serial-${arch}.log" 2>&1 | tee "${OUT}/deploy-${arch}.err" >&2
	report="$(grep 'bundle .*' "${OUT}/deploy-${arch}.err" | grep "${arch}" || true)"
	[ -n "${report}" ] || fail "deploy report for ${arch} has no bundle line naming ${arch}"
}

boot "${host_arch}" "${TIMEOUT}"
boot "${foreign_arch}" "${FOREIGN_TIMEOUT}" --arch "${foreign_arch}"

echo "==> e2e OK: ${MARKER} seen on the serial console for ${host_arch} and ${foreign_arch}" >&2
