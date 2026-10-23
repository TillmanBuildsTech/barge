# barge

Deploy containers to your own machines, safely and repeatably.

barge reads one YAML file, works out what would change on each target, shows
you, and then applies it with automatic rollback. A target is the machine
you are on or a remote Linux host over SSH. The container runtime is
configurable (Docker or Podman), so your config does not depend on which one a
host uses.

> **Status: pre-1.0.** The core is tested, but the config format and output may
> still change between minor versions, and barge has not yet been used widely in
> production. Read [Known limitations](docs/limitations.md) before relying on it.

## What it does

- **Plan before apply.** `barge plan` shows `create`, `replace`, `start`,
  `cleanup` or `blocked` for every app on every target and changes nothing.
- **Safe replace.** The new version is created under a temporary name first, so
  a bad spec or image fails before anything is stopped. Then the old version is
  swapped out; if the new one fails to start or does not stay running, the
  previous version is restored. Ctrl-C mid-deploy rolls back too. (There is a
  short gap while the swap happens.)
- **Fail closed.** If any target is unreachable, a secret is missing or an app
  is blocked, nothing is changed anywhere.
- **Only touches what it owns.** barge labels the containers it creates and
  refuses to modify or delete any it did not.
- **Secrets stay out of the config.** You reference an environment variable or
  a file; values are masked in all output.
- **SSH with verified host keys.** There is no option to skip verification.
- **Scriptable and interactive.** `plan` and `apply` have `-json` output and
  stable exit codes. `barge tui` is an interactive view of the same engine.

## Install

Download an archive from the
[Releases](https://github.com/TillmanBuildsTech/barge/releases) page, verify it
as described in [Verifying a release](docs/verifying-releases.md), and put
`barge` on your `PATH`. Binaries are static and available for Linux and macOS
(amd64, arm64) and Windows (amd64).

Or build from source (Go 1.26 or newer):

```
go install github.com/TillmanBuildsTech/barge/cmd/barge@latest
```

A source build reports its version as `dev`.

You also need `docker` or `podman` on every host you deploy to, and your user
must be allowed to run it. For SSH targets that user effectively has root on the
host; see the [threat model](docs/threat-model.md).

## Quick start

Save as `barge.yaml`:

```yaml
version: 1
runtime:
  driver: docker
apps:
  - name: web
    image: nginx:1.27
    restart: unless-stopped
    ports:
      - {host: 8080, container: 80}
    env:
      LOG_LEVEL: info
    secrets:
      API_KEY: {env: WEB_API_KEY}
targets:
  - name: dev
    type: local
    apps: [web]
```

Then:

```
export WEB_API_KEY=...            # the secret lives in your environment, not the file
barge validate -config barge.yaml -resolve-secrets
barge plan     -config barge.yaml
barge apply    -config barge.yaml
```

Run it again and it reports `unchanged`. Change the image tag and `plan` shows a
`replace`. To deploy to a remote host, add a target with `type: ssh`; see
[Configuration](docs/configuration.md).

## Documentation

| | |
| --- | --- |
| [Commands](docs/commands.md) | every command and flag, output formats, exit codes, CI use, troubleshooting |
| [Configuration](docs/configuration.md) | every field of `barge.yaml` |
| [Secrets](docs/secrets.md) | how secrets are referenced, resolved and protected |
| [Interactive UI](docs/tui.md) | `barge tui` |
| [Threat model](docs/threat-model.md) | what barge protects, and what it does not |
| [Verifying a release](docs/verifying-releases.md) | checksums, signatures, provenance, SBOMs |
| [Known limitations](docs/limitations.md) | what is not supported yet |

## Security

Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).

## License

[Mozilla Public License 2.0](LICENSE). Changes you make to barge's own files
must stay under the MPL when you distribute them; you can combine barge with
code under other licenses.
