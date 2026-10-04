# Contributing

Thanks for considering a contribution to contemper.

## Reporting bugs and requesting features

Use the [issue tracker](https://github.com/contemper-project/contemper/issues)
for bug reports and feature requests - search for an existing issue
first. For a bug, include contemper's version, your OS and
architecture, and enough of the image or command line to reproduce the
problem. Security vulnerabilities are the one exception: report those
privately, following [SECURITY.md](SECURITY.md), never as a public
issue.

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

For a local coverage report:

```sh
go test -coverprofile=coverage.out -covermode=atomic ./...
go tool cover -html=coverage.out
```

Run the linter before opening a PR:

```sh
make lint
```

This downloads the [golangci-lint](https://golangci-lint.run/) version
pinned in `.golangci-lint-version` (the same one CI uses) into `bin/` on
first run, then runs it with the linters and formatters configured in
`.golangci.yml`, plus [actionlint](https://github.com/rhysd/actionlint)
on the workflow files under `.github/workflows/`. `make actionlint` runs
just the latter.

When you add or change functionality, add tests for it in the same
change: unit tests in the relevant package, or a boot test under
`hack/e2e*.sh` when the change touches disk, boot, or support-image
behavior (see Boot tests below). Reviewers expect new functionality to
arrive with coverage, not as a follow-up.

### Boot tests

`make e2e` and `make e2e-variants` build the Alpine example image,
convert it, and boot the result with QEMU, checking for a boot marker
on the serial console. They need a container engine (podman or docker),
the host tools above, and QEMU with UEFI firmware for your
architecture. `e2e-variants` also needs `curl` and a local registry
(it starts one on `localhost:5555` unless `E2E_REGISTRY` says
otherwise). Run these if you change disk, boot, or support-image
behavior; both scripts document their requirements in more detail at
the top of `hack/e2e.sh` and `hack/e2e-variants.sh`. Set
`CONTEMPER_E2E_COVERDIR=<dir>` to also collect integration coverage from
the `contemper` binary these scripts build and run.

#### Distribution entries

`test/distros/matrix.json` lists the distribution images that are
converted and booted, each built from the family's Containerfile under
`test/distros/` with the entry's base image and build args. To run one
entry locally:

```sh
hack/e2e.sh --distro fedora-44
```

The image prints `contemper-boot-ok` only if the in-guest check
(`test/distros/common/contemper-check.sh`) passes, and a
`contemper-check-failed:` line with the reason otherwise; the script
stops waiting as soon as it sees one.

Docker Hub limits anonymous pulls per address, which a run over many
entries can exceed. `--registry-mirror HOST` (or
`CONTEMPER_E2E_REGISTRY_MIRROR=HOST`), for example `mirror.gcr.io`,
pulls Docker Hub base images through that mirror instead; other
registries are not affected.

The `Distributions` workflow (`.github/workflows/distros.yml`) runs every
entry nightly, and again for each release. It is not part of the CI
checks that gate pull requests. When an entry fails, the workflow opens
one issue for it and architecture, labelled `distro-test-failure` and
`area: target-distros`, with the run, the stage that failed (build,
convert or boot) and the base image digest. Later failing runs add a
comment; once the entry passes again the issue is closed with a comment.
Issues are matched by a hidden marker in their body, so leave it in
place when editing one. The `distro-test-failure` label has to exist in
the repository.

To run entries on demand, use "Run workflow" on the Distributions
workflow, or:

```sh
gh workflow run distros.yml --ref my-branch -f ids=fedora-44,debian-13 -f arch=amd64
```

`ids` is a comma-separated list of entry ids (empty runs all of them) and
`arch` is `amd64`, `arm64` or `all`. A manual run does not touch issues
unless `-f report=true` is given.

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

## Planning and priorities

Open work is tracked in GitHub issues and the
[contemper project](https://github.com/orgs/contemper-project/projects/1).
Issues have a type - Epic, Feature, Task, or Bug - and epics have
sub-issues, ordered by priority, that are the actual work. The project
also carries Priority (P1/P2/P3) and Size (S/M/L) fields, and groups
issues into milestones named after a theme rather than a version -
[release-please](https://github.com/googleapis/release-please) cuts
whichever release follows a merge, so a milestone doesn't promise a
specific version. The repository's issues list can't show a custom
order - the project's default table view can: milestones top to
bottom in the order they'll be worked, and inside each one epics
first, then P1, P2, P3. That view is the source of truth for what's
next. Issues without a milestone are later: accepted ideas that
aren't scheduled yet.

For features and tasks, priority means:

- **P1** - needed for the milestone's goal.
- **P2** - wanted in the milestone, but not blocking it.
- **P3** - nice to have, or for later.

An issue labelled `design` needs a decision recorded in
[docs/design](docs/design/) before the work it unblocks can start.

### Picking the next item

1. Open P1 bugs first.
2. Finish what's already In Progress before starting something new.
3. Otherwise, work through the first milestone in the project's
   order, top to bottom: a `design` issue before the work it
   unblocks, then by priority, preferring items that aren't blocked
   on something else.
4. Small items (Size S) are good for filling gaps between larger
   work.

### Bug triage

New bug reports arrive with type Bug and a `triage` label. A
maintainer reproduces the bug or asks for more detail, sets its
Priority, adds it to the project, and removes `triage`.

- **P1** - security issues (reported privately through
  [SECURITY.md](SECURITY.md), never as a public issue), data loss or
  corruption (wrong disk or volume contents), a correctly authored
  image that no longer converts or boots, a regression from the
  previous release, or broken release artifacts or install packages.
  These come before any feature work and release as soon as the fix
  lands - a `fix:` commit makes release-please cut a patch release.
- **P2** - a documented behavior that's broken but has a workaround,
  or only affects an uncommon setup. Goes into the current milestone.
- **P3** - cosmetic problems, unclear messages, docs mistakes. No
  milestone; picked up when convenient, often labelled
  `good first issue`.

If a bug report turns out to be a missing feature, its type changes
to Feature and it's prioritised like one.

### Before you start

Comment on an issue before starting anything larger than Size S, so
work isn't duplicated. Issues labelled `good first issue` or
`help wanted` are good entry points. Labels starting with `area:`
say which part of contemper an issue touches.

## Pull requests

PRs must pass CI (build, lint, tests, and the docs build) before merge.

## License

By contributing, you agree your contribution is licensed under the
Apache License 2.0 (see [LICENSE](LICENSE)), on the same terms as the
rest of the project.

### Developer Certificate of Origin

Every commit must carry a `Signed-off-by` trailer. Signing off certifies
you wrote the change or otherwise have the right to submit it under the
project's license, per the
[Developer Certificate of Origin](https://developercertificate.org/) -
there's no separate CLA to sign.

Add the trailer by committing with `git commit -s` (or `--signoff`),
using your configured `user.name` and `user.email`:

```sh
git commit -s -m "fix(disk): handle sparse files in the ext4 populator"
```

A CI check rejects any commit in a PR that's missing one, or whose
trailer doesn't match its author. If that happens, sign off the
existing commits and force-push:

```sh
git rebase --signoff origin/main
git push --force-with-lease
```
