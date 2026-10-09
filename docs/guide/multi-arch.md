# Multi-architecture

**contemper's own step needs no emulation.** There is no guest code to
run, so producing an arm64 disk on an x86-64 host is not a special case.

Building the image in the first place may well need emulation, since
`RUN` steps do execute guest code. That's handled by `docker buildx`, podman or
native runners, at a stage the container ecosystem already solves well.
The point isn't that emulation disappears; it's that it stays confined
there instead of reappearing at conversion time.

Architecture handling uses standard OCI mechanisms. Source images and
support images alike resolve from image indexes, so no `-amd64`/`-arm64`
tag conventions are needed, and mismatches fail at manifest level rather
than at boot. Only the assembler is architecture-aware, and only for the
UEFI boot path and the EFI stub; the kernel and initrd are already in
your image.

## Choosing the architecture

`convert` builds for the host architecture by default. `--arch amd64` or
`--arch arm64` picks another. If the source is a multi-platform index,
the matching manifest is selected before anything is fetched, and a
missing match is an error.

## Converting several architectures at once

`--arch` also takes a comma-separated list, or `all`:

```console
$ contemper convert --target qemu --arch all -o _out oci:./my-appliance
$ contemper convert --target qemu --arch amd64,arm64 -o _out oci:./my-appliance
```

- `all` converts every supported architecture (linux/amd64 and
  linux/arm64) the source's index provides. A single-platform source
  converts that one platform, if it is supported, and anything else is
  an error.
- A list converts exactly the architectures named, in a fixed order
  (amd64, then arm64) whatever order you wrote them in. Listing one
  twice, or naming an architecture the source lacks, is an error, and
  the check happens against the index before anything is fetched or
  converted.

Each architecture is a full, independent conversion, exactly what a
single-architecture run of it would do: its own support image and
volume-helper resolution, validation, bundle and manifest. They run one
after another, each under its own heading in the progress output. If one
fails, `convert` stops, names the architecture that failed and exits
non-zero; bundles already finished stay where they are.

A `docker-daemon:` source holds one architecture per run: its platforms
can't be listed without a `docker save`, so `all` and lists are rejected
for it. Pass `--arch amd64` or `--arch arm64`, or push (or `docker save`)
the multi-platform image and convert that instead.

A `containers-storage:` source is the same: one architecture per run,
`--arch amd64` or `--arch arm64`.

`contemper build` builds one architecture at a time, with either docker
buildx or podman, and rejects a list or `all`. To get every architecture,
build a multi-platform image yourself, push it to a registry, and run
`convert --arch all` on the reference:

=== "podman"

    ```console
    $ podman build --platform linux/amd64,linux/arm64 \
        --manifest registry.example.com/my-appliance:dev .
    $ podman manifest push registry.example.com/my-appliance:dev \
        docker://registry.example.com/my-appliance:dev
    $ contemper convert --target qemu --arch all registry.example.com/my-appliance:dev
    ```

=== "docker"

    ```console
    $ docker buildx build --platform linux/amd64,linux/arm64 --push \
        -t registry.example.com/my-appliance:dev .
    $ contemper convert --target qemu --arch all registry.example.com/my-appliance:dev
    ```

A manifest list that exists only in podman's local storage can't be
converted with `--arch all`. To convert a single architecture without a
registry, use `podman save --format oci-archive` on the image for that
architecture and convert the archive.

### Output

The bundles are siblings in `--out`, named as always
(`my-appliance-dev.x86_64/`, `my-appliance-dev.aarch64/`). stdout carries
one bundle path per line, in the same fixed order, so a script can read
them all.

A run given a list or `all` also writes a **group file**,
`<name>.multiarch.json` (here `my-appliance-dev.multiarch.json`), next to
the bundles, listing each architecture's bundle by relative path. It is
written whenever you ask for a list or `all`, even if that resolves to
one architecture, so scripts get a stable artifact; a plain
`--arch amd64` or the default writes none. A new run replaces the file.

An earlier group file of the same name is removed as soon as the run has
loaded its first image and so knows the bundle's name, and that goes for
a single-architecture run too, since its bundle replaces one the group
file lists (the report says when it removed one). A run that fails after
that point therefore leaves no group file. One that fails earlier, for
instance because the source can't be read or lacks a requested
architecture, leaves an existing group file as it was. All architectures
of a run must resolve to the same bundle name; if they don't, the run
stops, since one group file can't describe them. See the [bundle
reference](../reference/bundle.md#group-file) for the format.

## Deploying from a group file

`deploy --to local-qemu` accepts the group file in place of a bundle
directory and boots the bundle for the host's architecture:

```console
$ contemper deploy --to local-qemu _out/my-appliance-dev.multiarch.json
```

The report names the bundle it chose. `--arch amd64` or `--arch arm64`
picks a specific one instead; a bundle for a foreign architecture runs
under software emulation, so it is slow. If the group has no bundle for
the host and no `--arch` is given, deploy fails and lists the
architectures the group does have. It also fails if a bundle's manifest
disagrees with the architecture the group file lists it under, which
means the files are stale: convert again.

Everything else behaves as if you had passed the chosen bundle directory:
volumes and boot-test flags work as usual. The default instance name comes
from the source repository recorded in the bundle, so it is the same for
every architecture of a group, and so is the instance's volume state:
booting the amd64 bundle and then the arm64 one under the default name
reuses the same volume disks, and fails if a volume's size differs from
the existing disk's. Pass `--name` to
keep the architectures apart, and don't boot both at once under the same
name.
