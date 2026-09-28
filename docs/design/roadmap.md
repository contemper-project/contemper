# Roadmap

What contemper ships today is in the
[changelog](https://github.com/contemper-project/contemper/blob/main/CHANGELOG.md)
and the rest of these docs, starting with [how conversion
works](../guide/how-it-works.md) and [deploying locally](../guide/deploying.md).
This page only tracks what's still open.

## Next

### Lifecycle adapters

- A build adapter: build the image with `podman`/`docker` and convert in
  one command, chaining `build` → `convert` → `deploy --to local-qemu`.
- Publish and deploy to a remote Incus.

### Incus

- Publish the Incus support image (`incus-agent`) and have the `incus`
  target use it by default.
- Incus volumes and the deployment metadata that ties a bundle to an
  Incus instance.

### Volumes and the root filesystem

- Defined behavior when the image already has content at a `VOLUME`
  path.
- Faster root filesystem population.

### Distribution

- apt/yum package repositories.
- A tool vendoring strategy for release builds.

### API and configuration

- A project configuration file for phase defaults.
- A public Go library API, with the CLI as a thin wrapper over it.

### Open design decisions

- Whether to bless a set of base images that already satisfy contemper's
  fixed-path requirements (see [open questions](open-questions.md)).
