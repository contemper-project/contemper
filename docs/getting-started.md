# Getting started

This walks through the example image in the repository: an Alpine
appliance with OpenRC that boots to a serial login. You build it with
your usual container tooling, convert it, and boot it under QEMU.

## Install

contemper is a single Go binary plus a handful of host tools it discovers
rather than bundles (see [Host tools](reference/host-tools.md) for what
each one is for). Pick whichever of these gets it and those tools onto
your machine with the least fuss:

=== "Homebrew (macOS)"

    ```console
    $ brew install contemper-project/tap/contemper
    ```

    This installs `e2fsprogs` and `qemu` automatically (`convert` needs
    both; `qemu` also carries the UEFI firmware `deploy --to local-qemu`
    needs), and removes the quarantine attribute from the (unsigned)
    binary so macOS will run it.

=== "Debian / Ubuntu"

    Download the `.deb` matching your architecture from the
    [releases page](https://github.com/contemper-project/contemper/releases):

    ```console
    $ sudo apt install ./contemper_0.1.0_amd64.deb
    ```

    `apt` installs `e2fsprogs` and `qemu-utils` automatically, which is
    everything `convert` needs. For `deploy --to local-qemu`, add the
    emulator and UEFI firmware for the bundles you boot:

    ```console
    $ sudo apt install qemu-system-x86 ovmf            # amd64 bundles
    $ sudo apt install qemu-system-arm qemu-efi-aarch64  # arm64 bundles
    ```

=== "Fedora / RHEL"

    Download the `.rpm` matching your architecture from the
    [releases page](https://github.com/contemper-project/contemper/releases):

    ```console
    $ sudo dnf install ./contemper-0.1.0-1.x86_64.rpm
    ```

    `dnf` installs `e2fsprogs` and `qemu-img` automatically, which is
    everything `convert` needs. For `deploy --to local-qemu`, add the
    emulator for the bundles you boot; each pulls in its UEFI firmware:

    ```console
    $ sudo dnf install qemu-system-x86-core      # amd64 bundles
    $ sudo dnf install qemu-system-aarch64-core  # arm64 bundles
    ```

=== "Other Linux / manual"

    Works on any Linux distribution: download the tarball for your
    platform from the
    [releases page](https://github.com/contemper-project/contemper/releases)
    and put the `contemper` binary it contains on your `PATH`:

    ```console
    $ tar -xzf contemper_0.1.0_linux_amd64.tar.gz contemper
    ```

    Then install the host tools yourself. `convert` always needs
    `e2fsprogs` and a `qemu-img` binary; `deploy --to local-qemu`
    additionally needs a system emulator and UEFI firmware for the
    architecture you're deploying (not necessarily your host's own — see
    [Host tools](reference/host-tools.md) for cross-architecture deploys):

    - **macOS (Homebrew):** `brew install e2fsprogs qemu`
    - **Debian/Ubuntu:** `sudo apt install e2fsprogs qemu-utils` to
      convert; add `qemu-system-x86 ovmf` (amd64 bundles) or
      `qemu-system-arm qemu-efi-aarch64` (arm64 bundles) for
      `deploy --to local-qemu`
    - **Fedora/RHEL:** `sudo dnf install e2fsprogs qemu-img` to convert;
      add `qemu-system-x86-core` (amd64 bundles, pulls in `edk2-ovmf`) or
      `qemu-system-aarch64-core` (arm64 bundles, pulls in `edk2-aarch64`)
      for `deploy --to local-qemu`
    - **Other distributions:** install your package manager's equivalents
      of `e2fsprogs` and `qemu-img`, plus a system emulator and UEFI
      firmware if you'll use `deploy --to local-qemu`; see
      [Host tools](reference/host-tools.md) for exactly what each is for.

=== "From source"

    ```console
    $ go install github.com/contemper-project/contemper/cmd/contemper@latest
    ```

    This still needs the host tools listed under "Other Linux / manual"
    above; only the binary itself comes from Go. `contemper version`
    reports whatever version `go install` resolved (a pseudo-version if
    there is no tagged release yet) rather than the exact commit and
    build date a downloaded binary embeds.

For building the example below you also need podman or docker.

### Shell completion

The Homebrew cask and the `.deb`/`.rpm` packages install bash, zsh and fish
completions for you automatically. If you're running the bare binary from a
tarball, set it up yourself:

=== "bash"

    ```console
    $ mkdir -p ~/.local/share/bash-completion/completions
    $ contemper completion bash > ~/.local/share/bash-completion/completions/contemper
    ```

    or source it directly from your shell profile:
    `source <(contemper completion bash)`.

=== "zsh"

    ```console
    $ mkdir -p ~/.zfunc
    $ contemper completion zsh > ~/.zfunc/_contemper
    ```

    Then add `fpath=(~/.zfunc $fpath)` to `~/.zshrc`, before the line that
    runs `compinit`. Any other directory on `$fpath` works too.

=== "fish"

    ```console
    $ mkdir -p ~/.config/fish/completions
    $ contemper completion fish > ~/.config/fish/completions/contemper.fish
    ```

See `contemper completion <shell> --help` for shell-specific details.

### Verifying downloads

Every release archive and package carries a signed build provenance
attestation, so you can check that what you downloaded was actually built
by this project's release workflow, straight from that commit and
workflow run:

```console
$ gh attestation verify contemper_0.1.0_linux_amd64.tar.gz --repo contemper-project/contemper
```

(with the archive, `.deb` or `.rpm` name you downloaded).

That's on top of the usual checksum check against the release's
`checksums.txt`, which lists every archive and package and is itself
attested the same way.

## Build the example image

The example lives in `examples/alpine/Containerfile`. It installs a
kernel, generates a generic initrd, writes a kernel command line and
enables OpenRC, all at the paths contemper expects:

=== "podman"

    ```console
    $ podman build -t contemper-example:dev examples/alpine
    $ podman save --format oci-archive -o example.tar contemper-example:dev
    ```

=== "docker"

    ```console
    $ docker build -f examples/alpine/Containerfile -t contemper-example:dev examples/alpine
    $ docker save -o example.tar contemper-example:dev
    ```

    Docker doesn't look for a file named `Containerfile` on its own, so
    pass `-f` explicitly. Docker 25 and later write `docker save` output
    as an OCI layout, which the `oci-archive:` prefix below reads
    directly; with an older Docker, read the same file with
    `docker-archive:` in place of `oci-archive:`.

## Convert it

```console
$ contemper convert --target qemu oci-archive:example.tar -o _out
```

contemper checks the image is marked ready, merges its layers, checks
the kernel, initrd and command line are in place, and assembles a
UEFI-bootable qcow2 disk. The result is a *bundle*: a directory holding
the disk and a `contemper.json` manifest. `convert` prints the bundle's
path on stdout, and its progress on stderr.

## Boot it

```console
$ contemper deploy --to local-qemu _out/contemper-example-dev.aarch64/
```

This boots the disk under QEMU (hardware-accelerated where the host
allows it) and streams the serial console to your terminal. The disk is
booted with a throwaway overlay, so the bundle itself stays unchanged.
Log in as `root` with no password.

To use the boot as a test, have contemper wait for a line on the serial
console and exit once it appears. The example prints `contemper-boot-ok`
when OpenRC finishes starting:

```console
$ contemper deploy --to local-qemu _out/contemper-example-dev.aarch64/ \
    --expect contemper-boot-ok --timeout 180s
```

That is exactly what `make e2e` (`hack/e2e.sh`) and the CI boot test do.

## Next steps

Read [Authoring an image](guide/authoring.md) to adapt this to your own
image, starting from any base and any init system. `examples/debian` is
the same walkthrough with systemd as init instead of OpenRC, if that's
closer to what you're starting from, and `examples/archlinux` does the
same on Arch Linux (amd64 only).
