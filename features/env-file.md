# Env files for lanes (`.env.swim`)

Status: proposed

## Summary

Declarative `KEY=VALUE` files that give lanes their environment, in three
scopes: general (every repo), one repo, and one lane in a repo. swim parses
them; it never sources them. They replace `.lane.N.rc`.

```ini
# ~/.config/swim/env.swim : general, plus repo sections
STAGE=dev
AWS_REGION=us-east-1

[repo ~/dev/orders]
AWS_PROFILE=orders-prod
```

```ini
# <repo>/.env.swim : gitignored, lane sections only
[lane 2]
TABLE=orders-v2
```

## Motivation

- **Context is scattered:** non-secret context (`STAGE`, `AWS_PROFILE`,
  region, table names) lives in the operator's shell or is hard-coded per
  round, so the same round behaves differently depending on who runs it.
- **`.lane.N.rc` has a hole.** `lane_init` sources it as bash
  (`internal/assets/lib.sh:33`), and that causes three problems:
  - **It can approve guards silently:** `export FIN_ALLOW_DROP=1` in the rc
    makes `guard` approve (`lib.sh:219`), with no signature, no prompt and no
    `lookup.yml` rule.
  - **The chain can't see it:** a round entry hashes the script, not the rc,
    so one script hash can hide different behaviour.
  - **Workers can write it:** it sits in the repo directory, and only
    convention stops a worker editing it.

## Behaviour

### Files and precedence

- **General and repo scope** go in `~/.config/swim/env.swim`
  (`$XDG_CONFIG_HOME`): top-level keys, then `[repo PATH]` sections. Workers
  can't reach them, consistent with personal config.
- **Lane scope** goes in `<repo>/.env.swim`, which contains `[lane N]`
  sections only. `swim init` adds it to the `.gitignore` block.
- **CI defaults:** a committed `swim.yml` may hold an `env:` map of shared
  non-secret defaults.
- **Precedence, highest first:** the shell's env, then lane, then repo, then
  `swim.yml` `env:`, then general. What you type beats what's in a file.

### Format

- **Syntax:** `KEY=VALUE` lines, `#` comments, optional single or double
  quotes around a value.
- **No code:** no `export`, no `$VAR` expansion, no `$(...)`. A line that
  doesn't parse is an error that stops the run before any lane starts, and
  names the file and line.

### Rules

- **No guard flags.** A key that a selected script declares as a guard
  (header or `guard` call), or that matches a guard pattern in `lookup.yml`
  (for example `FIN_ALLOW_*`), is refused. The run stops, and `swim lint` and
  `swim doctor` report it. Approvals keep coming only from the shell,
  signatures, the prompt, `-y` or a queued batch.
- **Every value is masked.** Values from env files join the secret masker
  regardless of their name. The docs still say not to store secrets there:
  store `AWS_PROFILE`, not the key.
- **Recorded in the log:** the round header lists the env files that applied
  (`env: ~/.config/swim/env.swim[repo], .env.swim[lane 2]`), and
  `header_env` works as today.
- **Recorded in the chain:** a round entry gains `env_sha256`, a hash of the
  resolved file-sourced env (sorted `KEY=VALUE`). Same script plus same env
  hash means same inputs.
- **Queued batches pin it.** `swim queue` resolves and hashes each lane's
  env at queue time. If the hash differs when the batch runs, that lane is
  skipped, like an edited script.

### Commands

- **`swim env [N|JOB] [--yaml]`:** the resolved env for a lane, masked,
  with the source of each key (`shell`, `lane 2`, `repo`, `swim.yml`,
  `general`).
- **`swim doctor --fix`:** migrates `.lane.N.rc` files into `[lane N]`
  sections, when they're plain assignments. Anything else is reported for
  hand migration.

### Deprecating `.lane.N.rc`

- **For one breaking version:** `lane_init` still sources the rc, with a
  `WARN` in the log. A guard flag set by the rc is refused.
- **Then removed:** the next breaking version drops it.

## Design

- **Parser and resolver:** a new `internal/envfile` package that parses
  files into ordered `(key, value, source, line)` entries and resolves them
  by precedence for a repo and lane.
- **Launcher:** `internal/launcher/launcher.go:506` builds the lane's env
  from `os.Environ()`. Merge the resolved file env underneath it there, and
  pass the masker the merged env (`launcher.go:528`).
- **Guard check:** before exec, intersect the file-sourced keys with the
  lane's guards (`lane.Info`) and `lookup.yml` patterns
  (`internal/lookup`). Reuse it in `internal/lint/checks.go`.
- **Chain:** add `EnvSHA256` to `chain.Entry`
  (`internal/chain/chain.go:45`). This is a new optional field, so older
  entries still verify.
- **Queue:** add `EnvSHA256` beside `SHA256` on a queued lane
  (`internal/queue/queue.go:41`), and check it at claim time.
- **Config:** add `env` to the keys that `swim.yml` accepts.

## Open questions

1. **Named profiles:** add `[scope prod]` sections, chosen with
   `swim run --env prod`? Switching to prod is a high-stakes change, so a
   profile choice should appear in the round header, and maybe needs a
   guard of its own.
2. **Repo sections:** are paths in the user file enough, or should a repo
   section also match by git remote, so clones in other places pick it up?
3. **Who may write `.env.swim`:** should a lane's own section be writable
   by its worker (the env hash makes edits visible), or is the file
   operator-only, enforced by `lint`?
4. **Is this breaking?** Does refusing guard flags from `.lane.N.rc` need a
   breaking bump and `swim lock --help` notes?

## Testing

- **Parser:** quoting, comments, malformed lines (file and line in the
  error), duplicate keys (last wins, with a lint warning).
- **Precedence:** a key set in every layer resolves to the shell's value,
  then lane, then repo, then `swim.yml`, then general, as each layer is
  removed.
- **Guards:** a guard name, or one matching a `lookup.yml` pattern, in any
  file stops the run before any lane starts. The same flag in the shell
  still approves.
- **Masking:** a file value with an innocent name (`FOO=hunter2`) never
  appears in logs, status or YAML.
- **Chain:** `env_sha256` changes when a value changes, and `swim verify`
  passes on chains with and without the field.
- **Queue:** editing a lane's env after queueing skips that lane.
- **Migration:** `doctor --fix` converts a plain `.lane.N.rc`. An rc
  setting a guard flag is refused with a clear error.
- **e2e:** a scenario running lanes across all scopes, offline.

## Out of scope

- **Secret managers:** no `op://` or SSM references. Rounds use the
  operator's existing credentials.
- **Expansion or interpolation** of values.
- **Per-step env:** env stays per lane.
