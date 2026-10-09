# Getting started

This walks through the example image in the repository: an Alpine
appliance with OpenRC that boots to a serial login. Two commands get you
there: `contemper build` turns the example's `Containerfile` into a
bundle, and `contemper deploy --to local-qemu` boots it under QEMU.

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

Building the example below with `contemper build` needs Docker with
buildx or podman. [Build by hand](#build-by-hand) works with either
engine without it.

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

The release also carries the attestations as Sigstore bundles
(`checksums.txt.sigstore.json` for `checksums.txt`, and
`contemper_<version>_provenance.sigstore.json` for every archive and
package), so you can verify offline after downloading the bundle next to
the file:

```console
$ gh attestation verify contemper_0.1.0_linux_amd64.tar.gz \
    --bundle contemper_0.1.0_provenance.sigstore.json --repo contemper-project/contemper
```

That's on top of the usual checksum check against the release's
`checksums.txt`, which lists every archive and package and is itself
attested the same way.

## Build and boot the example

The example lives in `examples/alpine/Containerfile`. It installs a
kernel, generates a generic initrd, writes a kernel command line and
enables OpenRC, all at the paths contemper expects.

`contemper build` runs the build with docker buildx or podman (see
[Choosing the build engine](#choosing-the-build-engine)) and converts the
result in one step. It picks up the `Containerfile` on its own when the
directory has no `Dockerfile`:

```console
$ contemper build --target qemu -o _out examples/alpine
_out/alpine-dev.aarch64
```

The image is tagged after the directory (`alpine:dev`), and the bundle
is named after the tag and the architecture (`x86_64` on an x86-64
host). A *bundle* is a directory holding a UEFI-bootable qcow2 disk and
a `contemper.json` manifest. The build's progress, the engine's own
output included, goes to stderr; stdout carries only the bundle's path.

Boot it:

```console
$ contemper deploy --to local-qemu _out/alpine-dev.aarch64
```

This boots the disk under QEMU (hardware-accelerated where the host
allows it) and streams the serial console to your terminal. The disk is
booted with a throwaway overlay, so the bundle itself stays unchanged.
Log in as `root` with no password.

To use the boot as a test, have contemper wait for a line on the serial
console and exit once it appears. The example prints `contemper-boot-ok`
when OpenRC finishes starting. Since `build` prints only the bundle's
path, the two commands also chain:

```console
$ contemper deploy --to local-qemu "$(contemper build --target qemu -o _out examples/alpine)" \
    --expect contemper-boot-ok --timeout 180s
```

### Choosing the build engine

`--engine auto` is the default. It uses Docker when `docker buildx` works
and podman otherwise. A `docker` command that is really podman's docker
emulation counts as podman, since it has no buildx. `--engine docker` or
`--engine podman` skips the detection and uses just that engine; if it
isn't usable, `build` fails and prints the command to run by hand.

The two engines build with these commands, and load the image into their
own local image store:

=== "podman"

    ```console
    $ podman build --platform linux/arm64 -t alpine:dev \
        -f examples/alpine/Containerfile examples/alpine
    ```

    The converted image is read back with a `containers-storage:` source.
    On macOS, podman needs a running podman machine.

=== "docker"

    ```console
    $ docker buildx build --load --platform linux/arm64 -t alpine:dev \
        -f examples/alpine/Containerfile examples/alpine
    ```

    The converted image is read back with a `docker-daemon:` source.

(The platform is the host's, or whatever `--arch` names.)

To build without converting, pass `--image-only`. It stops after the
build and prints the image tag on stdout; `--target` is not needed. Pass
that tag on to `convert` with the source prefix that matches the engine:

=== "podman"

    ```console
    $ tag=$(contemper build --engine podman --image-only examples/alpine)
    $ contemper convert --target qemu "containers-storage:$tag" -o _out
    ```

=== "docker"

    ```console
    $ tag=$(contemper build --engine docker --image-only examples/alpine)
    $ contemper convert --target qemu "docker-daemon:$tag" -o _out
    ```

See [source references](reference/sources.md) for the `docker-daemon:` and
`containers-storage:` sources.

## Build by hand

Prefer this if you want an OCI archive to keep or push rather than
loading the image straight into the engine's local image store.
These are the steps `build` runs for you: build and save the image with
your container engine, then convert the archive.

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

```console
$ contemper convert --target qemu oci-archive:example.tar -o _out
```

contemper checks the image is marked ready, merges its layers, checks
the kernel, initrd and command line are in place, and assembles a
UEFI-bootable qcow2 disk. `convert` prints the bundle's path on stdout,
and its progress on stderr, the same way `build` does. Boot it the same
way as above:

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
