#!/usr/bin/env bash
# Writes the bash, zsh and fish completion scripts GoReleaser ships in every
# release artifact (see the `before.hooks` entry in .goreleaser.yaml), by
# asking the contemper binary itself to print them via its Cobra-provided
# `completion` command. Output is deterministic (no timestamps or build-host
# paths), so it doesn't affect reproducible builds.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="${repo_root}/completions"

mkdir -p "${out_dir}"

go run "${repo_root}/cmd/contemper" completion bash >"${out_dir}/contemper.bash"
go run "${repo_root}/cmd/contemper" completion zsh >"${out_dir}/_contemper"
go run "${repo_root}/cmd/contemper" completion fish >"${out_dir}/contemper.fish"

echo "wrote ${out_dir}/contemper.bash, ${out_dir}/_contemper, ${out_dir}/contemper.fish" >&2
