# Roadmap

What exists, and the rough shape of what's left before a v1.

## Done

- [x] Readiness check, layer merge, fixed-path validation
- [x] Registry, OCI archive, OCI layout and docker archive sources, with
      per-platform selection
- [x] Plain root-filesystem support images and `requires.files`
- [x] UKI assembly with an embedded systemd-stub
- [x] GPT disk with ESP and ext4 root, built without root privileges;
      qcow2 output
- [x] `qemu` and `incus` targets with aliases
- [x] Bundle manifest
- [x] `deploy --to local-qemu`, with boot tests in CI
- [x] Preserve extended attributes and file capabilities in the root
      filesystem

## Next

- [ ] Finalize the support-image annotation schema (branch and variant
      declarations, default variants) and implement variants
- [ ] Publish the Incus support image
- [ ] Build, publish and deploy adapters, one backend each (buildx for
      build, incus for publish and deploy)
- [ ] Project configuration file for phase defaults
- [ ] Volumes: guest-side formatting on first boot, fstab entries,
      volume and root disk sizes
- [ ] Resolve the deployment metadata question
- [ ] Tool vendoring strategy for release builds
- [ ] Decide on blessed base images
