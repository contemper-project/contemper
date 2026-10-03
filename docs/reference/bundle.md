# Bundle manifest

`convert` produces a **bundle**, not a bare disk file: a directory holding
the disk and a `contemper.json` manifest beside it.

```text
my-appliance-v3.aarch64/
├── contemper.json
└── disk.qcow2
```

The directory is named `<repository>-<tag>.<arch>`, using the machine
architecture names `aarch64` and `x86_64`.

The manifest is there because the disk alone can't answer the questions
deploying needs answered: which images it came from, which target was
resolved, what volumes the image declared, what ports it exposed. It's
plain JSON in a plain directory, so `cat` is a perfectly good inspection
tool.

## Fields

| Field | Meaning |
| --- | --- |
| `formatVersion` | manifest format version, currently `1` |
| `contemperVersion` | the contemper version that built the bundle |
| `createdAt` | build time, UTC |
| `source.ref`, `source.digest` | the source reference as given, and the digest of the image used |
| `source.repo` | the source image's repository name, no tag; `deploy --to local-qemu`'s default instance name |
| `support.ref`, `support.digest` | the support image, if one was merged |
| `support.origin` | where `support.ref` came from: `"target"` (the resolved target's own default) or `"flag"` (`--support` on the command line) |
| `support.variants` | each of the support image's branches' resolved variant: `branch`, `variant`, and (unless it was a no-op) `ref`/`digest` for the image that won |
| `volumeHelper.ref`, `volumeHelper.digest`, `volumeHelper.variants` | the automatically merged volume-formatting support image, in the same shape as `support` (without `origin`), if the source image declares volumes and `--no-volume-helper` wasn't given |
| `target` | the canonical target name, for example `qemu-qcow2`, never the alias |
| `boot` | how the image boots: `"uki"` (contemper assembled a UKI) or `"bootloader"` (the image's own bootloader is on the ESP, see [Bootloader images](../design/bootloader-images.md)). Manifests written before this field existed lack it; read them as `"uki"` |
| `secureBoot` | `true` when the image asked for UEFI Secure Boot with `io.contemper.secure-boot` (bootloader images only); omitted otherwise. `deploy --to local-qemu` then boots Secure Boot firmware, see [Secure Boot](../design/bootloader-images.md#secure-boot). Manifests written before this field existed lack it; read them as `false` |
| `arch` | the image architecture (`arm64`, `amd64`) |
| `disk.file`, `disk.format`, `disk.sizeBytes`, `disk.sha256` | the disk file and its checksum |
| `volumes` | one object per volume declared with `VOLUME`: `name`, `path`, `size` (bytes; omitted if unsized), `fs` (always `"ext4"`) |
| `hints.exposedPorts`, `hints.healthcheck` | from `EXPOSE` and `HEALTHCHECK`; inputs for deployment, not used for the disk |
| `reproducible` | `false` when the source was a local archive or layout |

`support`, `volumeHelper`, `volumes` and the entries under `hints` are
left out when empty; `hints` itself is always present.

!!! note "Why `support` and `volumeHelper` are separate"
    A bundle can carry two independent support-image merges: the one the
    user asked for with `--support`, and the one contemper adds on its
    own because the image declares volumes (see [the volumes
    guide](../guide/volumes.md)). Keeping them as two top-level fields,
    each shaped exactly like the other, means `support` always reflects
    only what the user asked for, `volumeHelper` always reflects only
    what contemper added, and a reader that only cares about one never
    has to filter a merged list by some added "role" field.

## Example

```json
{
  "formatVersion": 1,
  "contemperVersion": "v0.1.0",
  "createdAt": "2026-09-26T19:28:49Z",
  "source": {
    "ref": "oci-archive:_out/example.tar",
    "digest": "sha256:d3cc1ee83a78e8aa7e91d9c552e26ad53021f436e1b0e35aba1bb1b15b80f585",
    "repo": "example"
  },
  "volumeHelper": {
    "ref": "ghcr.io/contemper-project/volumes-support:v1",
    "digest": "sha256:1111111111111111111111111111111111111111111111111111111111111",
    "variants": [
      {
        "branch": "init-system",
        "variant": "openrc",
        "ref": "ghcr.io/contemper-project/volumes-support-init-system-openrc:v1",
        "digest": "sha256:2222222222222222222222222222222222222222222222222222222222222"
      }
    ]
  },
  "target": "qemu-qcow2",
  "boot": "uki",
  "arch": "arm64",
  "disk": {
    "file": "disk.qcow2",
    "format": "qcow2",
    "sizeBytes": 112656384,
    "sha256": "639ee78c1ce9ea132ecd93590405121a7f2f337438d49fa029482c8e542c6a9d"
  },
  "volumes": [
    { "name": "data", "path": "/data", "size": 10737418240, "fs": "ext4" }
  ],
  "hints": {},
  "reproducible": false
}
```

A bundle built with a support image records where its reference came
from:

```json
  "support": {
    "ref": "ghcr.io/contemper-project/incus-support:v1",
    "digest": "sha256:9f2c...",
    "origin": "target"
  },
```

`"origin": "flag"` records the same thing when `--support` on the command
line replaced the target's default instead.

The manifest doubles as the deployment metadata format and the
provenance record: the digests linking a disk to its inputs are recorded
because deploying needs them anyway.

## Converting into an existing bundle directory

`convert` writes the bundle into a subdirectory of `--out` named after
the image (`<repository>-<tag>.<arch>`). It assembles the whole bundle,
disk and `contemper.json`, in a temporary directory beside that
subdirectory and moves it into place only once everything succeeded. A
failed conversion leaves no partial bundle behind, and an existing bundle
of the same name stays as it was.

When the conversion succeeds, a previous bundle of the same name is
replaced as a whole, so files the new run doesn't produce (for example a
`disk.raw` from an earlier `--keep-raw` run) are gone afterwards. If that
subdirectory exists, is not empty and has no `contemper.json`, `convert`
refuses to touch it and fails before doing any work.

## Group file

A `convert` run given an `--arch` list or `all` (see
[multi-architecture](../guide/multi-arch.md)) also writes a group file
beside its bundles, so a script can find the one for a given
architecture. It is named `<name>.multiarch.json`, where `<name>` is the
bundles' shared `<repository>-<tag>`, and a later run replaces it.

```json
{
  "formatVersion": 1,
  "source": {
    "ref": "oci-archive:_out/example.tar",
    "repo": "example"
  },
  "bundles": [
    { "arch": "amd64", "path": "example-dev.x86_64" },
    { "arch": "arm64", "path": "example-dev.aarch64" }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `formatVersion` | group file format version, currently `1` |
| `source.ref`, `source.repo` | the source reference as given, and the source image's repository name, as in each bundle's manifest; there is no `source.digest` because every architecture resolves to its own image (each bundle's manifest records its digest) |
| `bundles[].arch` | the image architecture (`amd64`, `arm64`), at most once each |
| `bundles[].path` | the bundle directory, relative to the group file's own directory; never absolute and never leaving that directory |

The file lists at least one bundle, in amd64, arm64 order. Single-architecture
runs write no group file.

`contemper deploy --to local-qemu` reads the file when given its path
instead of a bundle directory, and boots the entry for the host's
architecture (or the one `--arch` names), after checking that the
bundle's own manifest has the same `arch`. A script must do the same:
resolve each `path` relative to the group file's directory, then check the
bundle manifest's `arch`.

!!! note "On \"no new artifact format\""
    contemper reads only normal OCI images. A bundle is an *output*, and a
    directory holding a disk and a JSON file barely qualifies as a format.
    Tar or OCI-artifact packaging of bundles are possible later additions
    if distribution needs them.
