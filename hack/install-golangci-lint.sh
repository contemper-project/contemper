#!/bin/sh
# Downloads a golangci-lint release binary into a directory, verifying
# the archive against the release's checksums file. `make lint` uses it
# to install the version pinned in .golangci-lint-version (the same one
# CI's lint job runs).
#
# Usage: hack/install-golangci-lint.sh <version, e.g. v2.14.0> <dir>
#
# This is a plain download plus checksum check rather than golangci-lint's
# install.sh piped into sh, so no downloaded code runs before it has been
# verified. golangci-lint publishes no signature for its checksums file,
# so the checksum guards against corrupt or truncated downloads; the
# archive and checksums file are both trusted to come from the GitHub
# release over HTTPS (as CI's golangci-lint-action does).
#
# Requires: curl, tar, sha256sum (or shasum -a 256 on macOS).
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: $0 <version> <dir>" >&2
	exit 2
fi
tag="$1"
dir="$2"

case "${tag}" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "error: version must look like vX.Y.Z, got '${tag}'" >&2
	exit 1
	;;
esac
version="${tag#v}"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*)
	echo "error: unsupported OS $(uname -s); install golangci-lint ${tag} yourself" >&2
	exit 1
	;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*)
	echo "error: unsupported architecture $(uname -m); install golangci-lint ${tag} yourself" >&2
	exit 1
	;;
esac

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	else
		echo "error: need sha256sum or shasum (macOS) to verify the download" >&2
		exit 1
	fi
}

name="golangci-lint-${version}-${os}-${arch}"
archive="${name}.tar.gz"
checksums="golangci-lint-${version}-checksums.txt"
base_url="https://github.com/golangci/golangci-lint/releases/download/${tag}"

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT

echo "downloading golangci-lint ${tag} (${os}/${arch})" >&2
curl -sSfL -o "${work_dir}/${archive}" "${base_url}/${archive}"
curl -sSfL -o "${work_dir}/${checksums}" "${base_url}/${checksums}"

want="$(awk -v f="${archive}" '$2 == f { print $1 }' "${work_dir}/${checksums}")"
if [ -z "${want}" ]; then
	echo "error: no checksum for ${archive} in ${checksums}" >&2
	exit 1
fi
got="$(sha256_file "${work_dir}/${archive}")"
if [ "${got}" != "${want}" ]; then
	echo "error: checksum mismatch for ${archive}: want ${want}, got ${got}" >&2
	exit 1
fi

tar -xzf "${work_dir}/${archive}" -C "${work_dir}" "${name}/golangci-lint"
mkdir -p "${dir}"
# Move into place last, so an interrupted run never leaves a partial
# binary behind for make to treat as up to date.
mv "${work_dir}/${name}/golangci-lint" "${dir}/golangci-lint.tmp"
mv "${dir}/golangci-lint.tmp" "${dir}/golangci-lint"
