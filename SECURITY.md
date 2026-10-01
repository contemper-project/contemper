# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately, using GitHub's private
vulnerability reporting: open the "Security" tab on this repository and
click "Report a vulnerability", or go directly to
<https://github.com/contemper-project/contemper/security/advisories/new>.
Do not open a public issue for a suspected vulnerability.

If you can't use GitHub's form, open a public issue asking for a private
contact channel - please don't include any vulnerability details in it.

## What to expect

- Acknowledgement within 7 days.
- An initial assessment - accepted or declined, and severity - within 14
  days.
- A fix or mitigation and coordinated public disclosure, via a GitHub
  Security Advisory (with a CVE where applicable), within 90 days of the
  report, earlier once a fix is released.
- If a deadline can't be met, we'll tell you why and give you a new date.

You'll be credited in the advisory unless you'd rather stay anonymous.

## Supported versions

Only the latest release is supported. Fixes land there; please upgrade
before reporting an issue that may already be fixed.

## Scope

contemper reads OCI images and host tool output to build VM disk images.
Things worth knowing when thinking about attack surface:

- contemper parses untrusted image content (manifests, layer tarballs,
  config files) but never executes anything from an image - there's no
  code path that runs a binary or script out of the image being
  converted.
- contemper shells out to host tools (`mkfs.ext4`, `debugfs`, `e2fsck`,
  `qemu-img`, `qemu-system-*`, ...) with fixed argv arrays; it does not
  build shell command lines from image content.
- Malformed or malicious image content causing a crash, resource
  exhaustion, or incorrect output is in scope. Vulnerabilities in the
  host tools contemper shells out to are out of scope for this repo -
  please report those upstream.
