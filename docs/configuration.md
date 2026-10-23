# Configuration reference (`barge.yaml`)

Barge reads one YAML file, passed with `-config`. This page lists every field,
its default and its validation rules. For what the commands do with the file,
see [commands.md](commands.md). For secrets, see [secrets.md](secrets.md).

Check a file without touching any host:

```
barge validate -config barge.yaml
```

## Layout

```yaml
# Top-level keys; all of them are described below.
version: 1        # required, must be 1
runtime: {}       # required: default container runtime
deploy: {}        # optional: rollout timing
apps: []          # what to run
targets: []       # where to run it (at least one)
```

- `apps` describe containers. `targets` say which machine each app goes to.
  An app is only deployed if some target lists it in its `apps`; one app can
  be listed by several targets.
- Decoding is strict. An unknown field anywhere is an error (for example
  `field bogus not found in type config.App`), and so is a second YAML
  document in the same file (`config must contain a single YAML document`).
  An empty file is an error (`config is empty: version is required`).
- Validation reports every problem it finds at once, not just the first.
- Loading the file never touches the network, the environment or your hosts.
  Secrets, SSH keys and `known_hosts` are only read by `plan` and `apply`
  (and `validate -resolve-secrets` for secrets).

## A complete local example

```yaml
version: 1

runtime:
  driver: docker

deploy:
  settle: 5s
  stop_timeout: 10s

apps:
  - name: web
    image: nginx:1.27
    restart: unless-stopped
    ports:
      - {host: 8080, container: 80}
    env:
      LOG_LEVEL: info
    labels:
      team: platform
    volumes:
      - {source: web-content, target: /usr/share/nginx/html, read_only: true}

targets:
  - name: dev
    type: local
    apps: [web]
```

## A complete SSH example

```yaml
version: 1

runtime:
  driver: docker

apps:
  - name: api
    image: ghcr.io/example/api:1.4.2
    command: ["serve", "--port", "8000"]
    restart: always
    ports:
      - {host_ip: 127.0.0.1, host: 8000, container: 8000}
    secrets:
      DATABASE_PASSWORD: {env: PROD_DATABASE_PASSWORD}

targets:
  - name: prod
    type: ssh
    runtime:
      driver: podman        # this host uses podman; others use the default
    ssh:
      host: prod.example.com
      port: 22
      user: deploy
      identity_file: ~/.ssh/id_ed25519
      known_hosts: ~/.ssh/known_hosts
      connect_timeout: 30s
    apps: [api]
```

## `version`

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `version` | integer | yes | none | Must be `1`. Anything else fails with `version must be 1 (got N)`. |

## `runtime`

The default container runtime for every target that does not set its own.

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `driver` | string | yes | none (no implicit default) | Must name a driver compiled into your binary. Run `barge drivers`; the built-in ones are `docker` and `podman`. |
| `options` | map of string to string | no | empty | Accepted and passed to the driver. The built-in `docker` and `podman` drivers do not use any options today. |

An unknown driver is reported before any host is contacted:
`error: runtime.driver: unknown runtime driver "rkt" (available: docker, podman)`.
The same check runs for each target's `runtime.driver`.

## `deploy`

How replacements are rolled out. The whole block is optional.

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `settle` | duration | no | `5s` | How long a new container must stay running after it starts. If it exits earlier, the deploy is rolled back. Must not be negative. |
| `stop_timeout` | duration | no | `10s` | Grace period given to the old container when it is stopped during a replace. Must not be negative. |

`0s` (or leaving the field out) means "use the default"; you cannot configure a
zero settle time. Both settings apply to every target.

### Duration syntax

Durations are strings with a unit, in Go's format:
`500ms`, `5s`, `30s`, `1m`, `1m30s`, `2h`. A bare number such as `settle: 5`
fails with `parse duration "5": time: missing unit in duration "5"`.

## `apps`

Each entry describes one container. The app `name` becomes the container name
on every target that deploys it.

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `name` | string | yes | none | Must match `^[a-zA-Z0-9][a-zA-Z0-9_.-]*$` (cannot start with `-`). Unique across `apps`. |
| `image` | string | yes | none | Must match `^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$`: no whitespace, cannot start with `-`. |
| `command` | list of strings | no | image default | Arguments passed after the image; each element is one argument. |
| `env` | map of string to string | no | empty | Plain, non-secret values. Names must match `^[A-Za-z_][A-Za-z0-9_]*$` and must not be reserved (see below). YAML numbers and booleans are accepted and stored as text. |
| `secrets` | map of name to reference | no | empty | `{env: NAME}` or `{file: PATH}`, exactly one. See [secrets.md](secrets.md). A name may not also be in `env`. |
| `labels` | map of string to string | no | empty | Container labels. Reserved keys are rejected (see below). |
| `ports` | list | no | none | See below. |
| `volumes` | list | no | none | See below. |
| `restart` | string | no | runtime default | One of `no`, `always`, `on-failure`, `unless-stopped`. Anything else: `restart policy "X" is not supported`. |

Errors for an app are prefixed with its position and name, for example
`apps[0] (web): ...`.

### `ports`

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `host` | integer | yes | none | 1-65535. |
| `container` | integer | yes | none | 1-65535. |
| `protocol` | string | no | `tcp` | `tcp` or `udp`. |
| `host_ip` | string | no | all interfaces (runtime default) | Address to bind on the host. Passed to the runtime as given. |

### `volumes`

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `source` | string | yes | none | A named volume or a host path, passed to the runtime as given. |
| `target` | string | yes | none | Absolute path inside the container (starts with `/`). |
| `read_only` | boolean | no | `false` | Mount read-only. |

`~` is not expanded in `source`; use an absolute path. For an SSH target the
path refers to the remote host.

### Reserved labels

Barge puts these labels on every container it creates and uses them to
recognise what it owns. You cannot set them: `managed-by`, and any key
starting with `barge.` (`barge.app` and `barge.spec-hash` are the ones barge
writes). The error is
`label "barge.x" is reserved for barge (managed-by, barge.*)`.

### Reserved environment names

Env and secret names that would change how the container CLI itself behaves
are refused, compared case-insensitively:
`PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `TMP`, `TEMP`,
`USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `SYSTEMROOT`, `WINDIR`, `COMSPEC`,
`PATHEXT`, `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`, and anything
starting with `DOCKER_`, `CONTAINER_`, `PODMAN_`, `BUILDAH_`, `XDG_`, `LD_`,
`DYLD_` or `SSH_`. The reason is explained in [secrets.md](secrets.md).

## `targets`

At least one target is required (`targets: at least one target is required`).

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `name` | string | yes | none | Unique. Used by `-target` and shown in output. |
| `type` | string | yes | none | `local` or `ssh`. |
| `runtime` | object | no | inherits top-level `runtime` | Same fields as [`runtime`](#runtime). |
| `ssh` | object | required for `ssh`, forbidden for `local` | none | See below. |
| `apps` | list of app names | no | none | Each name must exist in `apps` (`references unknown app "X"`). Deployed in the order listed. Listing the same app twice passes `validate` but fails `plan` with `duplicate app name`. |

A target with no `apps` is valid; `plan` prints `(no apps)` for it.

### Target types

- `local`: runs the container CLI on the machine barge runs on. Works on
  Linux, macOS and Windows. A `ssh` block on a local target is an error.
- `ssh`: runs the container CLI on a remote Linux host. Commands are sent as
  POSIX `sh` commands, so the host needs a POSIX shell and the container CLI
  for the chosen driver. Windows as an SSH target is not supported yet.

### Runtime inheritance

A target without `runtime.driver` uses the top-level `runtime` (driver and
options). A target that sets `runtime.driver` uses its own block entirely; its
`options` are not merged with the top-level ones. A target `runtime` block that
sets only `options` and no `driver` is replaced entirely by the top-level one. `barge plan` shows the
resolved driver next to each target, for example `target prod (podman, ssh)`.

### `ssh`

| Field | Type | Required | Default | Rules |
| --- | --- | --- | --- | --- |
| `host` | string | yes | none | Host name or address. |
| `user` | string | yes | none | No default user. |
| `identity_file` | string | yes | none | Path to a private key. `~/` is expanded. |
| `port` | integer | no | `22` | 1-65535. |
| `known_hosts` | string | no | `~/.ssh/known_hosts` | Path to a `known_hosts` file. `~/` is expanded. |
| `connect_timeout` | duration | no | `30s` | Must be positive. Bounds the TCP connect and the SSH handshake. |

SSH requirements, enforced when barge connects (during `plan`, `apply` and
`tui`, not by `validate`):

- **`identity_file` is required and must be a private key without a
  passphrase.** On Linux and macOS the file must not be accessible to group
  or others (`chmod 600`); otherwise barge refuses it with
  `identity file PATH has permissions 0644; it must not be accessible to group or others (chmod 600)`.
  Passphrase-protected keys fail with
  `identity file PATH is passphrase-protected, which is not supported yet`.
- **Host keys are always verified** against `known_hosts`, and there is no
  option to turn that off. The file must exist
  (`load known_hosts (host keys are always verified): ...`). A host that is
  not listed fails with `host key verification failed: HOST is not in known_hosts (verify its fingerprint ..., then add it, e.g. with ssh-keyscan)`.
  A key that differs from the recorded one fails with a possible
  man-in-the-middle warning. Neither is retried.
- Authentication is by public key only. ssh-agent is not supported yet; a
  missing `identity_file` fails validation with
  `ssh.identity_file is required (ssh-agent support is not implemented yet)`.

## `~` expansion

| Where | Expanded? |
| --- | --- |
| `targets[].ssh.identity_file` | Yes (`~` or `~/...`) |
| `targets[].ssh.known_hosts` | Yes |
| `apps[].secrets.*.file` | Yes |
| `-config` path | No (your shell expands it) |
| `apps[].volumes[].source` | No |

Only a leading `~` or `~/` is expanded, using the home directory of the user
running barge. `~user/...` is not supported.

## What triggers a replace

Barge stores a fingerprint of each app's spec in a label on the container.
`plan` compares it with the spec from your config and shows `replace` when
they differ. These fields are part of the fingerprint:

- `name`, `image`, `command`
- `env`, including the resolved values of `secrets` (rotating a secret
  redeploys the app)
- `labels` (yours only)
- `ports` and `volumes` (order matters)
- `restart`

Map ordering (`env`, `labels`) does not matter; list ordering does. Changing
only `deploy.*`, `runtime`, a target's `ssh` block or which targets list the
app does not change the fingerprint.

If the fingerprint matches but the container is not running, the action is
`start`, not `replace`. If it matches and the container runs, nothing happens.

Not supported yet:

- Image digest pinning. A mutable tag such as `:latest` is pulled only when
  barge creates or replaces the container. An unchanged spec is not
  re-pulled, so a new image behind the same tag is not noticed. Change the
  tag, or use an immutable tag.
- Removing an app from the file does not remove its container; barge does not
  prune.
- App-level health checks. The only check is that the container is still
  running after `deploy.settle`.
