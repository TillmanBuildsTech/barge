# The interactive UI (`barge tui`)

```
barge tui -config barge.yaml [-target NAME]
```

The UI shows what would change, asks you to confirm, then shows each app's
progress live. It is a front end to the same engine as `barge plan` and
`barge apply`, so every safety rule below applies identically.

It needs a real terminal on both stdin and stdout. In scripts and CI it refuses
to start (exit code 2) and tells you to use `plan` / `apply`, which are the
supported way to automate barge.

## What happens

1. **Planning.** On start barge resolves your secrets, connects to each target
   and works out what would change. Nothing on any host is modified.
2. **Review.** Each target is listed with its apps:

   | Symbol | Word | Meaning |
   | --- | --- | --- |
   | `+` | create | no container yet |
   | `~` | replace | the image or configuration changed (a rotated secret counts) |
   | `>` | start | the right version exists but is not running |
   | `=` | unchanged | already up to date |
   | `!` | blocked | cannot proceed safely, the reason is shown |

3. **Confirm.** `a` or Enter asks `Apply N changes to M targets? (y/n)`. Only `y`
   proceeds. Enter, Esc, `n` and `q` do not.
4. **Applying.** Every app shows its current step (`pull image`, `start new
   version`, `verify new version stays running`, ...).
5. **Done.** A summary and, for any failure, the full error and whether the
   previous version was restored.

## Safety rules

- If any target cannot be reached, a secret cannot be resolved, or any app is
  blocked, **nothing is applied anywhere**. The screen says why. Fix it, then press
  `r` to re-plan.
- A replacement that fails, or that does not stay running for `deploy.settle`,
  is rolled back to the previous version, exactly as with `barge apply`.
- **Ctrl+C while applying does not kill barge.** It cancels the work: the app in
  progress is rolled back, the remaining apps are skipped, and the screen stays
  open to show the outcome. `SIGTERM` behaves the same way. Elsewhere Ctrl+C quits.
- Secret values never appear on screen, in error text from the container CLI or a
  registry, or in the closing summary (they are masked as `***`).

## Keys

| Key | Action |
| --- | --- |
| `a`, Enter | review screen: start an apply (asks to confirm) |
| `y` | confirm the apply |
| `n`, Esc, `q` | at the confirmation: go back |
| `r` | re-plan (re-reads secrets and re-inspects every host) |
| `↑` `↓` `j` `k` | scroll |
| PgUp PgDn, `g` `G`, Home End | scroll by page, jump to top / bottom |
| Ctrl+C | while applying: cancel and roll back. Otherwise: quit |
| `q` | quit (while applying: same as Ctrl+C) |

## Exit code and what is left in your terminal

The UI uses the alternate screen, so it disappears when you quit. A short
plain-text summary is printed afterwards so a record stays in your scrollback:

```
barge: 1 applied, 1 failed
  FAILED dev/api: replace "api": start new version: ...
```

The exit code is `0` when the plan was clean and everything that was applied
succeeded, and `1` otherwise (problems in the plan, any failed or skipped app,
or a cancelled apply).

## Appearance

Colours come from your terminal's own 16-colour palette, and `NO_COLOR` is
respected. Nothing relies on colour alone: every state also has a symbol and a
word. Lines are truncated or wrapped to the terminal width and the list scrolls
when it is taller than the window.

## Not available yet

- Applying only some of the apps or targets from within the UI (use `-target`,
  or the config's `apps` lists, to narrow what the UI manages).
- Viewing container logs.
- Editing the config.
