# Image labels

Every `io.contemper.*` label a source image can set, read from the image
config the same way `io.contemper.ready` is (see [Authoring an
image](../guide/authoring.md)). A support image's own annotations are a
separate schema: see [Support image
annotations](support-image-annotations.md).

| Label | Meaning |
| --- | --- |
| `io.contemper.ready="true"` | required; marks the image as intended for contemper (checked before any layer is pulled) |
| `io.contemper.volume.<path>.size` | a declared volume's size (e.g. `10GiB`); `deploy --volume <path>=<size>` overrides it, and supplies one when it's missing |
| `io.contemper.volume.<path>.name` | a declared volume's name, overriding the one derived from its path (at most 16 bytes, `[a-z0-9-]`) |
| `io.contemper.volume.<path>.seed` | `"false"` opts the volume out of being seeded from the image's own content the first time its disk is formatted; any value but `"true"`/`"false"` is a convert error |
| `io.contemper.root.size` | overrides the root partition's default size; `convert --root-size` takes precedence over this when given |
| `io.contemper.fstab="false"` | opts out of the `LABEL=<name> <path> ext4 defaults,nofail 0 2` lines contemper otherwise appends per declared volume; `convert --no-fstab` does the same |

`<path>` in a volume label is the exact `VOLUME` path, including its
leading `/` — `io.contemper.volume./data.size`, not
`io.contemper.volume.data.size`. See [Volumes](../guide/volumes.md) for
how declaration, naming and sizing fit together.
