# Contributing

Thanks for considering a contribution to contemper.

## Building and testing

You need the Go version in `go.mod`, plus the host tools contemper
shells out to - see [docs/reference/host-tools.md](docs/reference/host-tools.md)
for what they are and where to get them.

```sh
go build ./cmd/contemper
go vet ./...
go test ./...
```

`make build`, `make vet` and `make test` do the same.

### Boot tests

`make e2e` and `make e2e-variants` build the Alpine example image,
convert it, and boot the result with QEMU, checking for a boot marker
on the serial console. They need a container engine (podman or docker),
the host tools above, and QEMU with UEFI firmware for your
architecture. `e2e-variants` also needs `curl` and a local registry
(it starts one on `localhost:5555` unless `E2E_REGISTRY` says
otherwise). Run these if you change disk, boot, or support-image
behavior; both scripts document their requirements in more detail at
the top of `hack/e2e.sh` and `hack/e2e-variants.sh`.

### Docs

The docs site is built with [uv](https://docs.astral.sh/uv/):

```sh
go run ./cmd/contemper gen-docs docs/reference/cli.md
uv run mkdocs build --strict
```

The CLI reference (`docs/reference/cli.md`) is generated from the
command tree by the hidden `gen-docs` command, not written by hand -
regenerate it after changing any command's flags or help text.
`uv run mkdocs serve` previews the site locally.

## Commit style

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):
a subject line of `type(scope): summary`, using one of `feat`, `fix`,
`perf`, `deps`, `docs`, `refactor`, `test`, `build`, `ci`, `chore` or
`revert` as the type, and the affected area as the scope, e.g.
`fix(disk): handle sparse files in the ext4 populator`. Keep commits
small and focused - one logical change each - with imperative-mood
subjects ("add", not "added"). A CI check enforces the format.

PRs are rebase-merged, so every commit in a PR ends up on `main`
individually and must follow this style on its own, not just the PR
title.

Releases are cut by merging the release PR that
[release-please](https://github.com/googleapis/release-please)
maintains from these commit types and scopes; you don't need to bump
versions or write changelog entries by hand.

## Pull requests

PRs must pass CI (build, vet, tests, and the docs build) before merge.

## License

By contributing, you agree your contribution is licensed under the
Apache License 2.0 (see [LICENSE](LICENSE)), on the same terms as the
rest of the project. There's no separate CLA to sign.
