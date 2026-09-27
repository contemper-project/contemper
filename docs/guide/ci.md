# In a CI pipeline

Everything before conversion is a conventional container pipeline, and
conversion itself is side-effect-free: it reads images and writes a
bundle, nothing more.

- **Build** with `docker buildx build --platform linux/amd64,linux/arm64`
  or equivalent. An ordinary container build; emulation handled here.
- **Label** the image with `LABEL io.contemper.ready="true"` in the build
  file.
- **Push** a normal multi-arch image index to your registry.
- **Sign and scan** as usual. cosign, Trivy, admission policies and
  retention rules all apply unmodified, because it's a plain OCI image.
- **Convert** with `contemper convert --target <target> <ref>` per
  target and architecture. No container runtime, no privileged builder,
  no emulation. It parallelizes trivially, since each conversion is
  independent and read-only against the source.
- **Boot-test** with `contemper deploy --to local-qemu --expect <marker>`
  on a runner with KVM.
- **Keep the bundle** as your provenance record. It links the disk to
  the source and support image digests it came from.

## Reproducibility

A bundle built from a registry reference records the image digest and
can be rebuilt from it. One built from a local archive or layout records
the path it was read from, which can't be fetched again, so its manifest
says `"reproducible": false`. Push when you want a bundle worth keeping.

## Example: GitHub Actions boot test

contemper's own CI boots the example image on every change. The job
enables KVM for the runner user, installs QEMU and OVMF, and runs
`hack/e2e.sh`:

```yaml
e2e:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v7
    - uses: actions/setup-go@v7
      with:
        go-version-file: go.mod
    - name: Enable KVM
      run: |
        echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' \
          | sudo tee /etc/udev/rules.d/99-kvm4all.rules
        sudo udevadm control --reload-rules
        sudo udevadm trigger --name-match=kvm
    - run: |
        sudo apt-get update
        sudo apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils ovmf
    - run: ./hack/e2e.sh --timeout 300s
```

A second job, `e2e-variants` (`hack/e2e-variants.sh`), boots the same
example with a dummy `--support` image to exercise variant resolution
end to end: it pushes a support image and its variants to a local
registry, asserts the resolved variants recorded in `contemper.json`,
and boots waiting for the winning variant's own marker alongside the
example's.
