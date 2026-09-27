# Deploying locally

`contemper deploy --to local-qemu <bundle-dir>` boots a bundle's disk
under QEMU on the machine you're on. It's the quickest way to check an
image boots, and the basis of automated boot tests.

```console
$ contemper deploy --to local-qemu _out/my-appliance-dev.aarch64/
```

- The disk is booted with `snapshot=on`: writes go to a throwaway
  overlay, so the bundle stays unchanged and can be booted again from
  scratch.
- Acceleration is picked automatically: HVF on macOS, KVM on Linux when
  `/dev/kvm` can be opened, software emulation otherwise. A bundle for
  another architecture than the host's always runs under software
  emulation.
- UEFI firmware is found in Homebrew's qemu and in the usual distro
  packages (OVMF, AAVMF, `qemu-efi-aarch64`). Where a distro ships a
  variable-store template, the VM gets a writable copy of it.
- The VM's serial console streams to your terminal.

## Boot tests

| Flag | Meaning |
| --- | --- |
| `--expect <string>` | exit 0 as soon as this string appears on the serial console, and stop the VM |
| `--timeout <duration>` | how long to wait for `--expect` before failing with the end of the console log |
| `--serial-log <file>` | also write the serial console to this file |

Have your image print a marker once it's up, for example from an OpenRC
`local.d` script or a systemd unit, and wait for it:

```console
$ contemper deploy --to local-qemu _out/my-appliance-dev.aarch64/ \
    --expect my-appliance-ready --timeout 180s --serial-log serial.log
```

## Other providers

`local-qemu` has no image catalog, so there is nothing to publish to;
deploy is the only step that applies.

!!! warning "Planned"
    `publish` and `deploy` for Incus, including remote Incus servers, are
    designed but not implemented. See
    [Design: lifecycle phases](../design/phases.md).
