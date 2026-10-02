# Embedded systemd-stub binaries

This directory holds the two UEFI stub binaries that `internal/uki`
embeds (via `//go:embed`) to build a Unified Kernel Image (UKI):

- `linuxaa64.efi.stub` - arm64
- `linuxx64.efi.stub` - amd64

Each is systemd-stub: the small UEFI PE binary systemd-boot and
systemd-ukify use as the base for a UKI. `internal/uki` appends
`.osrel`, `.cmdline`, `.initrd` and `.linux` PE sections to one of
these, in pure Go, without shelling out to anything. Embedding the
stub means contemper can build a UKI without `systemd-boot-efi`
installed on the host, and the same code path works unchanged on
macOS, where that package doesn't exist at all.

## Where they come from

`hack/fetch-stubs.sh` fetches both stubs from Debian's
`systemd-boot-efi` package, at the version pinned by
`SYSTEMD_BOOT_EFI_VERSION` and `SUITE` near the top of that script
(also recorded in [`NOTICE`](../../../NOTICE)), and writes this
directory's `SHA256SUMS`. It does not trust `deb.debian.org` outright:
it verifies the suite's `InRelease` signature against a local Debian
archive keyring, checks the `Packages` index against the hash listed
in that verified `InRelease`, and checks the `.deb` against the hash
and size listed in that verified `Packages` entry, before extracting
anything from it. See the comment at the top of the script for the
full chain and for why a Debian archive keyring (not one fetched over
the same channel) is required.

## License

systemd-stub is part of [systemd](https://github.com/systemd/systemd),
licensed LGPL-2.1-or-later; the full license text is in
[`LICENSE.LGPL-2.1`](LICENSE.LGPL-2.1), alongside the binaries it
covers. contemper's own code remains Apache-2.0 (see the repository's
[LICENSE](../../../LICENSE)); appending PE sections to the stub at
conversion time doesn't modify the stub's own code. See
[`NOTICE`](../../../NOTICE) for the full attribution.

## Verifying these files yourself

Any of the following confirm the binaries match what's claimed:

- Run `sha256sum -c SHA256SUMS` in this directory and compare against
  the committed checksums.
- Re-run `hack/fetch-stubs.sh` (needs `curl`, `gpgv` or `sqv`, a
  Debian archive keyring, and `dpkg-deb` or `ar`+`tar`; see the
  script's header for details) and diff its output against what's
  committed here - it rebuilds both files from scratch via the trust
  chain above.
- Download the pinned version's `systemd-boot-efi_<version>_<arch>.deb`
  from Debian's archive by hand, extract
  `usr/lib/systemd/boot/efi/linux{aa64,x64}.efi.stub` from it, and
  compare against the files here directly.

## CI

The lint job's "Verify embedded systemd-stub checksums" step runs
`sha256sum -c SHA256SUMS` in this directory on every change, so a
binary that doesn't match its checksum fails CI. It only catches a
mismatch between the two; it can't tell whether the checksum itself
was updated to match a maliciously-swapped binary, which is why the
fetch script's trust chain above matters more than the CI check.

## Updating the pinned version

1. Bump `SYSTEMD_BOOT_EFI_VERSION` (and `SUITE`, if needed) in
   `hack/fetch-stubs.sh` to a version actually present in that
   suite's archive - the script's header explains how to check this
   and what to do if the pinned version has aged out.
2. Re-run `hack/fetch-stubs.sh`.
3. Update the version in `NOTICE`.
4. Commit the updated binaries, `SHA256SUMS` and `NOTICE` together
   with the version bump, in one commit.

Renovate watches this version against the suite's archive and can open
a PR that edits the pin, but it cannot run the script itself; such a
PR still needs the script re-run and its output committed before
merging.
