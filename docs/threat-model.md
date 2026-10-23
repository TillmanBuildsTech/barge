# Threat model

This page says what barge is designed to protect, what it assumes, and what it
does not do. It describes barge as it is today.

## What barge does

barge runs the `docker` or `podman` command-line tool with commands it builds
from your config, either on this machine or on a remote Linux host over SSH. It
holds, briefly and in memory, any secrets you reference.

## Trusted inputs

barge trusts the following. If an attacker controls one of them, barge cannot
protect you:

- **The config file.** It chooses which images run, with which commands,
  environment, ports and volumes, on which hosts. Running `barge apply` on a
  config you did not write is the same as running that code on your targets. In
  CI, never apply a config taken from an untrusted pull request.
- **The machine running barge**, including its environment variables, `PATH`
  (barge runs the `docker` or `podman` it finds there), and the files you
  reference for secrets and SSH keys.
- **The container runtime and its host.** Access to the Docker or Podman socket
  is root-equivalent on that host. A remote SSH user who can run `docker` is
  effectively root there.
- **The registry and the images you reference.** barge does not verify image
  signatures and does not pin digests (see below).

## What barge defends against

| Threat | Defence |
| --- | --- |
| Connecting to an impostor host over SSH | Host keys are always checked against `known_hosts`; unknown or changed keys refuse to connect and nothing runs. There is no switch to disable it. A mismatch is reported as a possible man-in-the-middle and is never retried. |
| Shell injection through names, images, env values or arguments | Commands are built as argument lists, never shell strings. Locally there is no shell. Over SSH every argument is single-quoted for POSIX `sh` (fuzz-tested against a real `sh`). Names and image references that start with `-` or contain whitespace or metacharacters are rejected, and `--` precedes positional arguments. |
| Secrets in process listings | Values are never put on a command line. Locally they travel in the process environment; over SSH they are sent at the start of stdin. |
| Config choosing variables that steer the container tool (`DOCKER_HOST`, `PATH`, `LD_PRELOAD`, ...) | Those names are rejected for both `env` and `secrets`. |
| Secrets in config files | The config can only hold references; a literal where a reference belongs is rejected without echoing it. |
| Secrets in output | Known secret values of 4 or more characters are masked in text, JSON, the interactive UI and error text from the container tool or registry. Masking happens before text is wrapped or truncated. |
| A weak SSH key file | A private key readable by group or others is refused. |
| Damaging containers that are not yours | barge only modifies containers carrying its ownership labels, and refuses a plan that would collide with one that does not. |
| A half-finished deployment | Replacements are staged and rolled back on any failure, including cancellation. Any problem on any target stops the whole apply before changes begin. |
| Tampered releases | Signed checksums, provenance and SBOMs; see [verifying-releases.md](verifying-releases.md). |

## What barge does not defend against

- **Anyone who can read the container's environment.** Secrets are environment
  variables inside the container, visible to anyone with access to the runtime
  (`docker inspect`) and to root on the host. Delivering secrets as mounted
  files is not supported yet.
- **A compromised registry or malicious image.** Images are pulled by tag.
  barge does not verify image signatures or pin digests, so a changed tag
  changes what runs the next time barge pulls it. Pin versions you trust.
- **A compromised target host.** An attacker with root on a target can read its
  secrets and alter its containers.
- **Ownership labels are not an access control.** Anyone who can talk to the
  runtime can add or remove labels. They protect against mistakes, not
  attackers.
- **Time-of-check to time-of-use.** The host can change between `plan` and
  `apply`. barge's safeguards (ownership, rollback) still apply, but the plan
  you reviewed may not match what happens.
- **Resource exhaustion by a hostile host or container tool.** The output of
  the container tool is read into memory without a size cap.
- **Secrets that barge does not know about.** Masking works on values barge
  resolved. A secret that appears in a registry error but was never configured
  as a secret is not masked.
- **Windows or macOS as an SSH target**, and ssh-agent or passphrase-protected
  keys: not supported, so they offer no protection model.
- **A malicious `PATH`** on the machine running barge, or `DOCKER_HOST` set in
  your own shell (it is honoured for local targets, as it is for the `docker`
  tool itself).

## Supply chain

Dependencies are few and listed in `go.mod`; `govulncheck` runs on every push
and before every release, and releases are built with the newest Go patch
release so standard-library fixes apply. GitHub Actions are referenced by
version tag rather than by commit hash, and automated dependency updates are
configured for Go modules and Actions.
