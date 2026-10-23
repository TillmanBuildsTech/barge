# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately, in either of these ways:

1. Use GitHub's private reporting: the **Report a vulnerability** button on the
   [Security tab](https://github.com/TillmanBuildsTech/barge/security/advisories/new)
   of this repository.
2. Email **brandon@tillmanbuildstech.com** with "barge security" in the subject.

Include what you found, the barge version (`barge version`), and steps to
reproduce. A proof of concept helps but is not required.

You can expect an acknowledgement within 5 business days. barge is maintained
by one person, so there is no guaranteed fix time, but a confirmed
vulnerability is prioritised over everything else. You will be told what is
planned and credited in the advisory unless you prefer not to be.

## Supported versions

barge is pre-1.0. Only the latest release receives security fixes.

## Scope

In scope: anything that lets an attacker do more than the documented
[threat model](docs/threat-model.md) allows, for example:

- running commands or reading files on the machine running barge or on a target
  beyond what the configuration authorises;
- bypassing SSH host-key verification;
- secret values appearing in output, logs or command lines;
- barge modifying or deleting a container it does not own;
- a tampered or unsigned release artifact passing the verification steps in
  [verifying-releases.md](docs/verifying-releases.md).

Out of scope: problems that need control of the config file, of the machine
running barge, or of the container runtime (these are trusted inputs, see the
threat model), and the limitations already listed in
[limitations.md](docs/limitations.md).

## How releases are protected

Releases are built by GitHub Actions from a tagged commit, after the tests and
`govulncheck` pass. They ship with a checksum file, a keyless Sigstore signature,
SPDX SBOMs and a build provenance attestation. See
[verifying-releases.md](docs/verifying-releases.md).
