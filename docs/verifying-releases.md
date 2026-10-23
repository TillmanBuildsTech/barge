# Verifying a release

Every release is built by GitHub Actions from the tagged commit, never by hand.
Each release contains:

| File | Purpose |
| --- | --- |
| `barge_<version>_<os>_<arch>.tar.gz` / `.zip` | the binary, LICENSE, README, example config and docs |
| `checksums.txt` | SHA-256 of every archive |
| `checksums.txt.sigstore.json` | keyless cosign signature of `checksums.txt` |
| `*.sbom.json` | SPDX software bill of materials, one per archive |

Binaries are static (`CGO_ENABLED=0`), built with `-trimpath`, and reproducible:
the same commit produces the same bytes.

## 1. Check the archive against the checksums

```
sha256sum --check --ignore-missing checksums.txt
```

On macOS use `shasum -a 256 --check --ignore-missing checksums.txt`.

## 2. Check that checksums.txt was signed by this project's release workflow

Requires [cosign](https://docs.sigstore.dev/cosign/installation/) v2 or later.

```
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/TillmanBuildsTech/barge/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

This proves `checksums.txt` was produced by the `release.yml` workflow in
`TillmanBuildsTech/barge` on a version tag. A checksum file from anywhere else
fails.

## 3. Check the build provenance (optional)

With the [GitHub CLI](https://cli.github.com/):

```
gh attestation verify barge_<version>_<os>_<arch>.tar.gz --repo TillmanBuildsTech/barge
```

## Windows and macOS

The binaries are not code-signed with a Windows or Apple developer certificate,
so SmartScreen or Gatekeeper may warn on first run. The checks above are the
supported way to establish trust. Not supported yet: native OS code signing,
and distribution through package managers (Homebrew, Chocolatey, apt).
