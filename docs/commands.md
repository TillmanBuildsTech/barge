# Command reference

```
usage: barge <command> [flags]

commands:
  version                          print the version
  drivers                          list available runtime drivers
  validate -config F [-resolve-secrets]
                                   validate a config file; with -resolve-secrets also
                                   check every secret reference resolves
  plan     -config F [-target T] [-json]
                                   show what apply would change (changes nothing)
  apply    -config F [-target T] [-json] [-dry-run]
                                   deploy; -dry-run is the same as plan
  tui      -config F [-target T]   interactive plan, review and apply
                                   (needs a terminal; use plan/apply in scripts)

exit codes: 0 success, 1 failure, 2 usage error
```

Flags go after the command. They use Go's flag syntax: `-config F`,
`--config F` and `-config=F` are all accepted, and boolean flags are plain
(`-json`). Running `barge` with no command, or with an unknown command, prints
this usage text to stderr and exits with 2. For the config file format see
[configuration.md](configuration.md).

## Exit codes

| Code | Meaning | When |
| --- | --- | --- |
| `0` | Success | The command did what you asked. For `plan`/`apply`: every target planned cleanly with nothing blocked and, when applying, every app succeeded or was already up to date. |
| `1` | Failure | The config file cannot be read or is invalid; a named runtime driver does not exist in this binary; `validate -resolve-secrets` found an unresolvable secret; any target could not be planned (secret, SSH, runtime unreachable); any app is `blocked`; any app failed or was skipped while applying; the report could not be written. |
| `2` | Usage error | No command or an unknown command; a flag that does not parse (including `-h`); `-config` missing; `-target` names a target that is not in the config; `barge tui` without a terminal. |

Note that `plan` exits `1` when the plan has problems, but exits `0` when it
is clean even if it shows changes. Exit `0` does not mean "nothing to do"; read
the actions (see [CI usage](#ci-usage)).

The config is loaded before `-target` is checked, so a broken config reports
exit `1` even if `-target` is also wrong.

## `barge version`

Prints `barge VERSION`, for example `barge v1.2.0`. A binary built without a
stamped version prints `barge dev`. Takes no flags.

## `barge drivers`

Prints the runtime driver names compiled into this binary, one per line:

```
docker
podman
```

These are the values accepted for `runtime.driver`. Takes no flags.

## `barge validate`

```
barge validate -config barge.yaml [-resolve-secrets]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config PATH` | none, required | Path to the config file. |
| `-resolve-secrets` | off | Also resolve every secret reference of every app in the file, to prove each one can be read. Values are never printed. |

`validate` parses the file strictly, applies every rule in
[configuration.md](configuration.md), and checks that each runtime driver
named exists in this binary. It never contacts a host and does not open your
SSH keys or `known_hosts` file; those problems only show up at `plan`.

```
$ barge validate -config barge.yaml
barge.yaml: OK (1 apps, 2 targets)

$ barge validate -config barge.yaml -resolve-secrets
barge.yaml: OK (1 apps, 2 targets)
2 secrets resolved
```

With `-resolve-secrets`, the `OK` line is printed first. If a secret cannot be
resolved, each problem goes to stderr, nothing more is printed on stdout, and
the exit code is `1`:

```
barge.yaml: OK (1 apps, 2 targets)
error: app "web": secret "TOKEN": not found: environment variable "NOPE_TOKEN" is not set
```

All apps in the file are checked, not only those used by a target. See
[secrets.md](secrets.md) for the secret rules.

## `barge plan`

```
barge plan -config barge.yaml [-target NAME] [-json]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config PATH` | none, required | Path to the config file. |
| `-target NAME` | all targets | Only plan this target. An unknown name exits `2` and lists the available ones. |
| `-json` | off | Print a machine-readable report instead of text. See [JSON output](#json-output). |

`plan` resolves secrets, connects to each selected target, checks the runtime
and inspects existing containers. It changes nothing on any host. It never
pulls images, creates, starts, stops or removes containers.

```
$ barge plan -config barge.yaml
target dev (docker, local)
  ~ web              replace    image changed (nginx:1.26 -> nginx:1.27)
  = worker           unchanged  up to date

target prod (podman, ssh)
  + web              create     no container named "web"
```

Each target header shows the resolved runtime driver and the target type. Each
app line is a symbol, the app name, the action and a reason. Secret values
never appear in the reason.

If a target cannot be planned, its section shows the error instead of apps,
and the command ends with a line on stderr and exit code `1`:

```
target prod (docker, ssh)
  error: invalid: identity file /home/you/.ssh/id_ed25519 has permissions 0644; it must not be accessible to group or others (chmod 600)
error: the plan has problems (see above)
```

### Plan actions

| Symbol | Action (JSON) | Shown as | Meaning |
| --- | --- | --- | --- |
| `+` | `create` | `create` | No container with the app's name exists. `apply` pulls the image, creates the container, starts it and waits for it to settle. |
| `~` | `replace` | `replace` | A container barge owns exists, but its spec differs (image, command, env including resolved secrets, labels, ports, volumes or restart). The reason is `image changed (OLD -> NEW)` or `configuration changed`. `apply` does a staged swap with rollback. |
| `>` | `start` | `start` | The container's spec matches but it is not running (reason: `container is exited`, `container is created`, ...). `apply` starts it. |
| `=` | `noop` | `unchanged` | Spec matches and the container is running (reason: `up to date`). `apply` does nothing for it. |
| `-` | `cleanup` | `cleanup` | Spec matches and the container is running, but a `NAME-barge-old` backup from an earlier replace is still there (its removal failed). `apply` removes only that backup. See [Interrupted deployments](#interrupted-deployments). |
| `!` | `blocked` | `blocked` | Barge cannot proceed safely and a person must act. The reason says why. |

An app is `blocked` for one of two reasons:

- A container with that name exists but was not created by barge:
  `container "web" exists and is not managed by barge`. Barge only manages
  containers that carry its ownership labels, and never adopts, changes or
  deletes any other container. Rename or remove the existing one, or rename
  your app.
- A previous deployment was interrupted: see
  [Interrupted deployments](#interrupted-deployments).

Any blocked app makes `plan` exit `1`, and makes `apply` refuse to run.

## `barge apply`

```
barge apply -config barge.yaml [-target NAME] [-json] [-dry-run]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config PATH` | none, required | Path to the config file. |
| `-target NAME` | all targets | Only deploy this target. |
| `-json` | off | Machine-readable report. See [JSON output](#json-output). |
| `-dry-run` | off | Produce exactly the output of `plan` and change nothing. In the JSON report the `command` is still `apply` and `dry_run` is `true`. |

`apply` first plans every selected target, exactly like `plan`. It proceeds
only if every target planned cleanly with nothing blocked. It then deploys
targets in the order they appear in the config and apps in the order of each
target's `apps` list. Nothing is printed while it works; the full result is
printed when it finishes. Use `barge tui` if you want live progress.

```
$ barge apply -config barge.yaml
target dev (docker, local)
  web              applied    (replace)
  worker           unchanged

target prod (podman, ssh)
  web              applied    (create)
```

A failed app shows the error and what happened to the previous version:

```
target dev (docker, local)
  web              FAILED     replace "web": verify new version stays running: container is exited, expected it to keep running
    rolled back: the previous state was restored
  worker           applied    (replace)
    warning: deployed, but could not remove the previous version "worker-barge-old" (remove it manually): ...
```

### Fail-closed

If any selected target has a problem at planning time, `apply` changes nothing
anywhere, including on the healthy targets. A problem is: a secret that cannot
be resolved, a target that cannot be reached or authenticated, a runtime that
does not answer, an invalid spec, or any blocked app. You get the plan output
(each failing target shows its error) and, on stderr:

```
error: nothing was changed: fix the problems above and re-run
```

Exit code `1`. Fix the problem and run again.

Fail-closed covers planning only. Once `apply` starts changing things, an app
that fails is rolled back and reported, and the remaining apps and targets are
still attempted. Apps and targets that were already deployed stay deployed.
The final stderr line is then:

```
error: some apps failed to deploy (see above)
```

Plan and apply are not atomic: barge assumes nobody changes the host between
its inspection and its changes.

### Apply outcomes

| Outcome (JSON) | Text | Meaning |
| --- | --- | --- |
| `applied` | `applied    (ACTION)` | The action finished and the container stayed running for `deploy.settle`. |
| `unchanged` | `unchanged` | The plan said `noop`; nothing was done. |
| `failed` | `FAILED     ERROR` | A step failed. The error names the action, the app and the step, for example `replace "web": start new version: ...`. |
| `skipped` | `SKIPPED    ERROR` | Not attempted because the run was cancelled (Ctrl-C or SIGTERM). |

Extra fields on a result:

| Field | Meaning |
| --- | --- |
| `rolled_back` | `true` when a failure was followed by a rollback that restored the previous state. For a failed `create` it means the broken new container was removed. Text: `rolled back: the previous state was restored`. |
| `rollback_error` | Restoring the previous state itself failed. The host needs manual attention; the message names the containers involved. Text: `ROLLBACK FAILED: ...`. |
| `warnings` | Non-fatal problems on an app that still succeeded, such as failing to delete the previous version. A warning does not change the exit code. Text: `warning: ...`. |

A `failed` result with neither `rolled_back` nor `rollback_error` means no
rollback was needed: nothing had been changed yet (for example the image pull
or the creation of the new container failed while the old one kept running),
or the action was `start`, which has nothing to restore.

### What a replace does

For an app whose spec changed, barge swaps the container in these steps. The
names `NAME-barge-new` and `NAME-barge-old` are temporary containers.

1. Pull the new image.
2. Remove a leftover `NAME-barge-new` from an earlier crashed run, but only if
   barge owns it. Anything else with that name is a conflict and the app fails.
3. Create `NAME-barge-new` from the new spec. The old container is still
   running, so a bad spec fails here with no downtime.
4. Stop the old container (only if it was running), waiting up to
   `deploy.stop_timeout`.
5. Rename the old container to `NAME-barge-old`.
6. Rename `NAME-barge-new` to `NAME`.
7. Start `NAME`.
8. Wait `deploy.settle`, re-checking that it is still running. If it exits in
   that time, the replace fails.
9. Remove `NAME-barge-old`. If this fails, the app is still `applied` and a
   warning tells you so. The next `plan` shows a `cleanup` for that app, and
   `apply` removes the backup.

Between steps 4 and 7 the app is down, so a replace has a short gap. Zero
downtime deployments are not supported yet.

### How rollback works

If step 4 to 8 fails, barge restores the previous version:

1. Remove the new container (as `NAME` if it was already renamed, otherwise as
   `NAME-barge-new`).
2. Rename `NAME-barge-old` back to `NAME`.
3. Start it again, if it was running before and barge had stopped it.

If all of that succeeds the result is `failed` with `rolled_back: true`. If any
part fails, the result carries `rollback_error`:
`rollback incomplete, manual recovery needed (previous version may be "NAME-barge-old" or "NAME"): ...`.

Rollback runs to completion even after Ctrl-C, bounded by a 2 minute limit.

A failed `create` removes the container it made, so a broken first deployment
is not left behind. A failed `start` is just reported.

Not supported yet: image digest pinning. A mutable tag such as `:latest` is
pulled only when barge creates or replaces a container. If the spec is
unchanged the action is `unchanged` and the image is not re-pulled, so a new
image behind the same tag is not noticed.

### Not supported yet

- Removing containers for apps you delete from the config (no pruning).
- App-level health checks. Success means the container is still running after
  `deploy.settle`.
- Zero-downtime deploys.
- ssh-agent, passphrase-protected keys, and Windows as an SSH target.

## `barge tui`

```
barge tui -config barge.yaml [-target NAME]
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-config PATH` | none, required | Path to the config file. |
| `-target NAME` | all targets | Only manage this target. |

Opens the interactive plan, review and apply screen. It needs a terminal on
both stdin and stdout; otherwise it prints
`error: barge tui needs an interactive terminal on stdin and stdout; for scripts and CI use `barge plan` and `barge apply` instead`
and exits `2`. Config problems and an unknown `-target` are reported before
the screen is taken over. The exit code is `0` when the plan was clean and
everything applied succeeded, `1` otherwise. See [tui.md](tui.md).

## JSON output

`plan -json` and `apply -json` print one JSON document on stdout instead of
text. Secret values known to barge are masked as `***` in it, as in text mode.
Only the document goes to stdout; in JSON mode the one-line summary on stderr
is not printed, so use the exit code and `ok`.

The document has `"schema": 1`. Adding fields is not a breaking change, so
ignore fields you do not know. Removing or renaming a field, or changing its
meaning, bumps the schema number.

Complete example of `apply -json` where one target succeeded and one app
failed and was rolled back:

```json
{
  "schema": 1,
  "command": "apply",
  "dry_run": false,
  "ok": false,
  "targets": [
    {
      "name": "dev",
      "runtime": "docker",
      "apps": [
        {
          "app": "web",
          "action": "replace",
          "reason": "image changed (nginx:1.26 -> nginx:1.27)",
          "outcome": "failed",
          "error": "replace \"web\": verify new version stays running: container is exited, expected it to keep running",
          "rolled_back": true
        },
        {
          "app": "worker",
          "action": "replace",
          "reason": "configuration changed",
          "outcome": "applied",
          "warnings": [
            "deployed, but could not remove the previous version \"worker-barge-old\" (remove it manually): ..."
          ]
        },
        {
          "app": "cache",
          "action": "noop",
          "reason": "up to date",
          "outcome": "unchanged"
        }
      ]
    },
    {
      "name": "prod",
      "runtime": "podman",
      "apps": [
        {
          "app": "web",
          "action": "create",
          "reason": "no container named \"web\"",
          "outcome": "applied"
        }
      ]
    }
  ]
}
```

A target that could not be planned has an `error` and an empty `apps` array:

```json
{
  "name": "prod",
  "runtime": "podman",
  "error": "invalid: identity file /home/you/.ssh/id_ed25519 has permissions 0644; it must not be accessible to group or others (chmod 600)",
  "apps": []
}
```

### Top level

| Field | Type | Always present | Meaning |
| --- | --- | --- | --- |
| `schema` | integer | yes | Report format version, currently `1`. |
| `command` | string | yes | `plan` or `apply`. |
| `dry_run` | boolean | yes | `true` when nothing was applied on purpose: every `plan`, and `apply -dry-run`. `false` for an `apply` that was allowed to run, including one that was refused because of planning problems (then `ok` is `false`). |
| `ok` | boolean | yes | `true` when every target planned cleanly with nothing blocked and, if applied, no app failed or was skipped. Matches exit code `0`. |
| `targets` | array | yes | One entry per selected target, in config order. Empty only if there are no targets. |

### Target (`targets[]`)

| Field | Type | Always present | Meaning |
| --- | --- | --- | --- |
| `name` | string | yes | Target name from the config. |
| `runtime` | string | yes | Resolved runtime driver for this target. |
| `error` | string | no | Why the target could not be planned (secret, SSH, runtime, invalid spec). Absent when planning worked. |
| `apps` | array | yes | Per-app results in target order. Empty when `error` is set. |

### App (`targets[].apps[]`)

| Field | Type | Always present | Meaning |
| --- | --- | --- | --- |
| `app` | string | yes | App name. |
| `action` | string | yes | `create`, `replace`, `start`, `noop` or `blocked`. See [Plan actions](#plan-actions). |
| `reason` | string | when non-empty | Why the action was chosen. Never contains env values. |
| `outcome` | string | only after an apply | `applied`, `unchanged`, `failed` or `skipped`. Absent in plans and dry runs. |
| `error` | string | when the app failed or was skipped | The error text. |
| `rolled_back` | boolean | only when `true` | The previous state was restored after a failure. |
| `rollback_error` | string | only when rollback failed | Why the previous state could not be restored. |
| `warnings` | array of strings | only when there are any | Non-fatal problems. |

A blocked app appears with `action: "blocked"` and no `outcome`. When apply
was refused because of a problem elsewhere, every target is still listed with
its plan and without outcomes, `dry_run` is `false` and `ok` is `false`.

## Ctrl-C and signals

Barge handles `SIGINT` (Ctrl-C) and `SIGTERM` by cancelling its work, not by
exiting at once.

- During planning: the target reports a cancellation error, nothing has been
  changed, and the exit code is `1`.
- During `apply`: the command in flight is cancelled. If it was in the middle
  of a replace, rollback runs anyway (detached from the cancellation, up to 2
  minutes) so the host is not left half-switched. Apps that were not started
  yet are reported `skipped`. The exit code is `1`. Apps finished before the
  signal stay deployed.
- Because the signal only cancels the work, pressing Ctrl-C again does not
  kill the process. Wait for the cleanup to finish. Killing barge with
  `SIGKILL`, or losing the machine, can leave the temporary containers that
  the next section describes.
- In `barge tui`, Ctrl-C while applying behaves the same way; elsewhere it
  quits. See [tui.md](tui.md).

## Interrupted deployments

A leftover `NAME-barge-old` is handled automatically only when it is provably
debris: `NAME` is a running container barge owns, and the leftover is also
barge's. Then `plan` shows `cleanup` (or `replace`, if the spec also changed)
and `apply` removes the backup. In every other case the app is blocked, as
described below, because the backup may be the only good copy.

If barge is killed (or the machine dies) during a replace, after the old
container was renamed to `NAME-barge-old`, the next `plan` reports the app as
blocked:

```
  ! web              blocked    a previous deployment was interrupted: container "web-barge-old" still exists; inspect it, then rename it back to "web" or remove it, and re-run
```

Barge cannot know which container is the good one, so it refuses to guess and
`apply` changes nothing until you decide. To recover, on the affected target
(the host in `ssh.host`, or the local machine), with `docker` or `podman` as
your driver:

1. List what is there: `docker ps -a --filter name=web`. You may see `web`,
   `web-barge-new` and `web-barge-old`.
2. Decide which version you want to keep.
   - **Keep the previous version.** Remove the containers named `web` and
     `web-barge-new` if they exist (`docker rm -f web web-barge-new`), then
     `docker rename web-barge-old web`, and `docker start web` if it should be
     running.
   - **Keep the new version.** If `web` exists and is healthy, remove the
     leftover with `docker rm -f web-barge-old`. If `web` does not exist, rename
     the new one into place: `docker rename web-barge-new web`, then remove
     `web-barge-old`.
3. Run `barge plan` again. The block is gone once no `web-barge-old` exists.
   `apply` then reconciles the container with your config (it may show
   `replace` or `start`).

A leftover `NAME-barge-new` alone does not block anything: the next replace
removes it automatically, provided barge owns it.

## CI usage

Use `plan -json` to gate a pipeline and `apply -json` to deploy, and decide on
the exit code and `.ok`, not on the text.

```sh
# Fail the job if the plan has problems; keep the report as an artifact.
barge plan -config barge.yaml -json > plan.json || {
  status=$?
  echo "barge plan failed with exit code $status" >&2
  # Exit 2 or a config error (1) may leave plan.json empty: nothing to parse.
  [ -s plan.json ] && jq -r '.targets[] | select(.error) | "\(.name): \(.error)"' plan.json >&2
  exit "$status"
}

# Defence in depth: the exit code and .ok say the same thing.
jq -e '.ok' plan.json > /dev/null

# List what would change.
jq -r '.targets[] as $t | $t.apps[] | select(.action != "noop")
       | "\($t.name)/\(.app): \(.action) (\(.reason))"' plan.json

# Deploy.
barge apply -config barge.yaml -json > apply.json
```

Points to know:

- Exit code `0` means the plan is clean, not that it is empty. To detect
  "nothing to deploy", check that no app has an `action` other than `noop`
  (the `jq` filter above prints nothing).
- Exit `1` can come from a pipeline-level problem (a missing secret variable,
  an unreachable host, a blocked app), so a failed `plan` is normally a
  reason to stop the pipeline.
- A config or usage error prints to stderr and writes no JSON. Check the exit
  code before parsing.
- `plan` and `apply` read secrets from the environment of the process, so
  export them in the CI job from your CI secret store. Barge masks them in its
  output; do not print them yourself. See [secrets.md](secrets.md).
- `barge tui` refuses to run without a terminal and exits `2`.
- For SSH targets, the CI runner needs the key file with mode `0600` and a
  `known_hosts` file that already lists the host; barge never accepts an
  unknown host key.
- Run `barge validate -config barge.yaml` as a cheap first job step; it needs
  no network.

## Troubleshooting

Messages are shown as barge prints them; text in capitals is a placeholder.

| Message | Cause and fix |
| --- | --- |
| `error: open config: open PATH: no such file or directory` | Wrong `-config` path. Exit `1`. |
| `plan: -config is required` (also `apply:`, `validate:`, `tui:`) | Pass `-config`. Exit `2`. |
| `plan: unknown target "x" (available: dev, prod)` | `-target` must match a `targets[].name`. Exit `2`. |
| `error: PATH: invalid: parse: yaml: unmarshal errors: line N: field F not found in type config.App` | Unknown field in the config; decoding is strict. Check the spelling against [configuration.md](configuration.md). |
| `invalid: config must contain a single YAML document` | Remove the extra `---` document. |
| `invalid: parse: parse duration "5": time: missing unit in duration "5"` | Durations need a unit: `5s`. |
| `invalid: version must be 1 (got N)` | Set `version: 1`. |
| `error: runtime.driver: unknown runtime driver "rkt" (available: docker, podman)` | Use a name from `barge drivers`. The same check names `targets[N].runtime.driver`. |
| `invalid: apps[N] (NAME): label "barge.x" is reserved for barge (managed-by, barge.*)` | Rename the label. |
| `invalid: env name "PATH" is reserved: it would change how the container CLI itself behaves` | Pick another name, or set it in the image. See [secrets.md](secrets.md). |
| `invalid: parse: line N: secrets must be references such as {env: NAME} or {file: PATH}; literal values are not allowed in the config` | A secret was written inline. Use `{env: NAME}` or `{file: PATH}`. |
| `app "NAME": secret "KEY": not found: environment variable "VAR" is not set` | Export `VAR` in the environment barge runs in. |
| `... not found: file "PATH" does not exist` | Check the secret file path. |
| `runtime docker: unavailable: docker not found in PATH: ...` | The container CLI is not installed or not on `PATH` of the machine that runs it (local: yours; SSH: the remote user's `PATH`). |
| `runtime docker: unavailable: docker version: ...` with the CLI's own message | The CLI is installed but cannot reach its engine (daemon stopped, no permission on the socket). Fix it on that host. |
| `error: identity file: stat PATH: no such file or directory` | `ssh.identity_file` does not exist. |
| `invalid: identity file PATH has permissions 0644; it must not be accessible to group or others (chmod 600)` | `chmod 600` the key. Not enforced on Windows. |
| `invalid: identity file PATH is passphrase-protected, which is not supported yet` | Use a key without a passphrase. ssh-agent is not supported yet. |
| `load known_hosts (host keys are always verified): ...` | The `known_hosts` file is missing or unreadable. Create it, for example by connecting once with `ssh`. |
| `host key verification failed: HOST is not in known_hosts (verify its fingerprint SHA256:..., then add it, e.g. with ssh-keyscan)` | Verify the fingerprint out of band, then add it to `known_hosts`. There is no way to skip this check. |
| `host key verification failed: the key presented by HOST (SHA256:...) does not match known_hosts; this may be a man-in-the-middle attack` | Do not continue until you know why the key changed. If the host was legitimately reinstalled, remove the old entry and add the verified new one. |
| `unavailable: dial HOST:PORT: ...` | Host, port or network is wrong, or the connect timed out (`ssh.connect_timeout`). |
| `unavailable: ssh handshake with HOST:PORT: ...` | Authentication failed (wrong `user`, key not in the host's `authorized_keys`) or the server rejected the connection. |
| `container "NAME" exists and is not managed by barge` | A container with that name was not created by barge. Barge will not touch it; rename or remove it, or rename the app. |
| `a previous deployment was interrupted: container "NAME-barge-old" still exists; ...` | See [Interrupted deployments](#interrupted-deployments). |
| `invalid: duplicate app name "NAME"` | An app is listed twice in one target's `apps`. |
| `error: the plan has problems (see above)` | Printed by `plan` on stderr when any target failed to plan or any app is blocked. Exit `1`. |
| `error: nothing was changed: fix the problems above and re-run` | `apply` refused because of a planning problem. See [Fail-closed](#fail-closed). |
| `error: some apps failed to deploy (see above)` | At least one app `FAILED` or was `SKIPPED` while applying. Read its error and rollback status. |
| `verify new version stays running: container is exited, expected it to keep running` | The new container crashed within `deploy.settle`. Look at its logs on that host (the failed one was removed by the rollback, so reproduce with the same image and settings), or fix the image and apply again. Raise `deploy.settle` if the app needs longer to prove it is healthy. |
| `ROLLBACK FAILED: rollback incomplete, manual recovery needed (previous version may be "NAME-barge-old" or "NAME"): ...` | Follow the recovery steps in [Interrupted deployments](#interrupted-deployments). |
| `warning: deployed, but could not remove the previous version "NAME-barge-old" (remove it manually): ...` | The deploy succeeded. The next `plan` shows `cleanup` for the app and `apply` removes the backup, or remove it yourself. |
| `error: barge tui needs an interactive terminal on stdin and stdout; ...` | Run `barge tui` in a real terminal, or use `plan` / `apply`. Exit `2`. |
