# Secrets

Secrets are passwords, tokens and keys your containers need. Barge never wants
them in the config file: you declare **where to find** each one, and barge reads
it when it runs.

```yaml
version: 1
runtime:
  driver: docker
apps:
  - name: web
    image: ghcr.io/example/web:1.4.2
    env:
      LOG_LEVEL: info                     # plain, not secret
    secrets:
      DATABASE_PASSWORD: {env: PROD_DATABASE_PASSWORD}
      API_KEY: {file: ~/.config/barge/web-api-key}
targets:
  - name: dev
    type: local
    apps: [web]
```

The key (`DATABASE_PASSWORD`) is the environment variable name the container
sees. Each entry has exactly one source:

| Source | Meaning |
| --- | --- |
| `env: NAME` | the value of environment variable `NAME` **in the environment barge runs in** |
| `file: PATH` | the contents of the file at `PATH` (`~/` is expanded) |

A name may appear in `env` or in `secrets`, not both. Writing a literal value
where a reference belongs is rejected, and the error message does not repeat it.

## Rules for values

- Resolved **on the machine running barge**, never on the target host. For an
  SSH target the value is sent over the encrypted connection.
- Resolved on every `plan` and `apply`, so a missing secret is caught before
  anything changes. Only the apps on the targets you selected are resolved.
- File secrets: one trailing newline (`\n` or `\r\n`) is removed, symlinks are
  followed (Kubernetes and Docker secret mounts use them), and the file must be
  a regular file of at most 64 KiB.
- Empty values and values containing a NUL byte are rejected.
- Problems are reported together and name the secret and where it should come
  from, never its content.

## Rotating a secret

The secret value is part of what barge compares, so when a value changes
`barge plan` shows the app as `replace` (reason: *configuration changed*) and
`barge apply` redeploys it with the usual rollback protection. Plan output never
shows the old or new value.

## Checking your setup

```
barge validate -config barge.yaml                    # syntax only, works anywhere
barge validate -config barge.yaml -resolve-secrets   # also checks every reference resolves
```

`-resolve-secrets` prints how many secrets resolved, never their values.

## Where a secret can be seen

| Place | Visible? |
| --- | --- |
| The config file | No, only references |
| barge's output (text and `-json`) | No. Known secret values of 4+ characters are masked as `***`, including inside error text that came from the container CLI or a registry |
| Command lines (`ps`), locally and on the remote host | No. Values travel in the process environment (locally) or on stdin (SSH), never as arguments |
| `barge`'s own process environment and its child processes | Yes, to the same user and to root |
| The container's environment (`docker inspect`, `/proc/<pid>/environ`) | **Yes**, to anyone who can talk to the container runtime. Access to the Docker socket is root-equivalent, so treat it that way |

Secrets are delivered as environment variables. That is convenient but it is
not the strongest option. Delivering secrets as files inside the container is
planned; until then, do not rely on barge to hide a secret from someone who
already has access to the container runtime on the host.

## Variable names barge refuses

Because values are handed to the container CLI through its own environment,
some names would change how that CLI behaves instead of configuring your
container. These are rejected, for both `env` and `secrets`:

`PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `TMP`, `TEMP`,
`USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `SYSTEMROOT`, `WINDIR`, `COMSPEC`,
`PATHEXT`, `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`, and anything
starting with `DOCKER_`, `CONTAINER_`, `PODMAN_`, `BUILDAH_`, `XDG_`, `LD_`,
`DYLD_` or `SSH_` (compared case-insensitively).

If your application needs one of these, set it in the image instead.

## Not supported yet

- Secret managers (Vault, cloud KMS) and encrypted config values.
- Delivering a secret as a mounted file.
- Per-target overrides of a secret's source.
