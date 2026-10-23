# Known limitations and planned enhancements

Everything here works as described today; this page lists what barge does not
do yet, so you can plan around it. Nothing in this list is a promise of a date.

## Deploying

- **Short downtime on replace.** The old container must stop before the new
  one can claim the same host ports, so a replace has a gap of a few seconds,
  and a rollback restarts the old container (its uptime resets). Zero-downtime
  deploys would need a proxy or a second port to switch between.
- **Mutable tags are not re-pulled.** If the spec is unchanged, `:latest` (or
  any reused tag) is not pulled again, so a new image under the same tag does
  not deploy. Pin versions or digests. Resolving and comparing image digests is
  planned.
- **Removed apps are not pruned.** Deleting an app from the config leaves its
  container running. A reviewed, opt-in prune is planned.
- **"Healthy" means "stays running".** There are no application health checks
  yet, and a failed rollout reports the exit state but not the container logs.
- **Targets are processed one at a time**, and a plan is not atomic with its
  apply if the host changes in between.
- **Interrupted deployments need a person** when the previous version may be
  the only good copy (see [commands.md](commands.md#interrupted-deployments)).

## Configuration and `validate`

- **Zero durations become defaults.** `settle: 0s`, `stop_timeout: 0s` and
  `connect_timeout: 0s` are treated as "not set" and replaced by the defaults
  (5s, 10s, 30s). You cannot currently ask for no settle time.
- **`validate` is offline.** It does not check that SSH identity files and
  `known_hosts` files exist and are usable; `plan` does. A `validate -ssh`
  check is planned.
- **`runtime.options` is reserved.** It is rejected until a driver uses it.
- **Secrets are environment variables in the container.** Delivering secrets
  as mounted files, and Vault or cloud KMS sources, are planned. See
  [secrets.md](secrets.md).

## Security hardening

- **Unbounded output capture.** The output of the container tool is read into
  memory with no size cap, so a hostile or broken host could exhaust memory.
  A cap is planned.
- **No image verification.** Images are pulled by tag; signatures are not
  verified and digests are not pinned. See the [threat model](threat-model.md).
- **GitHub Actions are pinned by version tag, not commit hash.**

## Commands and automation

- **No exit code for "changes pending".** `plan` exits 0 when it shows pending
  changes and 1 only for problems, so CI has to read the `-json` output (see
  [commands.md](commands.md#ci-usage)). A dedicated flag is planned;
  exit code 2 is already taken by usage errors, so it needs a different value.
- **Usage and config errors print no JSON** even with `-json`; they go to
  stderr with a non-zero exit code.
- **Pressing Ctrl-C twice does not force an exit.** The first Ctrl-C cancels
  the run and barge waits (up to two minutes) for rollback to finish, because
  quitting in the middle could leave a host half-changed. A second Ctrl-C is
  ignored until then. Killing the process with `SIGKILL` skips the rollback.

## Interactive UI

- Cannot select individual apps or targets, view container logs, or edit the
  config. Use `-target` and the config's `apps` lists to narrow what it manages.
- Verified on Linux terminals only; it builds and its logic is tested on macOS
  and Windows, but its appearance there is unconfirmed.

## Targets and runtimes

- **SSH targets must be Linux.** Windows as an SSH target is not supported.
- **ssh-agent and passphrase-protected keys** are not supported; the key must
  be a plain file readable only by you.
- **Podman** is covered by the shared contract tests in CI, but has had less
  real-world use than Docker.

## Releases

- Binaries are signed through Sigstore and come with SBOMs and provenance (see
  [verifying-releases.md](verifying-releases.md)), but are not signed with a
  Windows or Apple developer certificate. Homebrew, Chocolatey and apt packages
  do not exist yet.
