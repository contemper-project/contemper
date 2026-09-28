# Getting started

This walks through the example image in the repository: an Alpine
appliance with OpenRC that boots to a serial login. You build it with
your usual container tooling, convert it, and boot it under QEMU.

## Install

contemper is a single Go binary. Download one for your platform from the
[releases page](https://github.com/contemper-project/contemper/releases),
or build it from a checkout:

```console
$ go build -o contemper ./cmd/contemper
```

It needs a few host tools, which it finds on `PATH` (and in Homebrew's
keg-only locations):

=== "macOS (Homebrew)"

    ```console
    $ brew install go qemu e2fsprogs
    ```

=== "Debian / Ubuntu"

    ```console
    $ sudo apt install e2fsprogs qemu-utils qemu-system ovmf
    ```

For building the example you also need podman or docker. See
[Host tools](reference/host-tools.md) for what each tool is used for.

### Verifying downloads

Every release archive carries a signed build provenance attestation, so
you can check that a downloaded archive was actually built by this
project's release workflow, straight from that commit and workflow run:

```console
$ gh attestation verify contemper_0.1.0_linux_amd64.tar.gz --repo contemper-project/contemper
```

(with the archive name you downloaded).

That's on top of the usual checksum check against the release's
`checksums.txt` (itself attested the same way).

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
closer to what you're starting from.
