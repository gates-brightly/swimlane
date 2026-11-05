# Config

All of swim's configuration lives in your home directory, outside every
repo, so workers can't change it: `~/.config/swim/config.yml`
(`$XDG_CONFIG_HOME/swim/config.yml`). Nothing in a repo configures swim; a
`swim.yml` left in one is ignored.

## Layers

A value comes from the last of these that sets it, key by key:

| Layer | Where | Applies |
|---|---|---|
| `default` | swim itself | always |
| `defaults` | `defaults:` | every repo |
| `repo` | `repos.NAME` | this repo, every branch and worktree |
| `repo/branch` | `repos.NAME/BRANCH` | this repo, on that branch |

**NAME** is the repo's name: the last part of its `origin` remote's URL,
without `.git` (`swimlane`, from `git@github.com:gates-brightly/swimlane.git`),
or the git toplevel directory's name when it has no `origin`. Every
worktree of a repo shares its origin, so they share `repos.NAME`.
**BRANCH** is the checked-out branch, matched exactly (no globs); on a
detached HEAD there's no branch layer. Lists that add up
(`blocked_commands`, `secret_env`, `secret_env_ignore`) collect every
layer's; `runners` merge per extension.

```yaml
defaults:
  max_parallel: 8                  # lanes at once (0: unlimited, the default)
  blocked_commands: ["terraform destroy"]   # no lane command may contain these (none by default)
  secret_env: [GITHUB_TOKEN]       # masked as *** everywhere swim writes
  secret_env_auto: true            # also *_TOKEN, *_SECRET, *_PASSWORD, *_API_KEY, ...
  secret_env_ignore: [CSRF_TOKEN]  # exempt from secret_env_auto
  secret_patterns_auto: true       # also AWS key ids, GitHub/Slack tokens, JWTs
  header_env: [STAGE, AWS_PROFILE] # recorded in the round header when set (none by default; never secrets)
  runtime: node --version          # recorded as the runtime version
  toolchain: . "$HOME/.nvm/nvm.sh" && nvm use >/dev/null
  interrupt: graceful              # graceful | immediate: the first Ctrl-C
  interrupt_grace: 5s              # force quit: SIGINT, SIGKILL after this
  term_grace: 10s                  # SIGTERM: graceful, then force after this
  signing: auto                    # auto | required | off
  signing_key: ~/.ssh/id_ed25519   # a .pub signs through ssh-agent
  queue_lease: 2m                  # a watcher's heartbeat, seen from other hosts
  claim_lease: 2h                  # how long a lane claim lasts
  watch_batches: 4                 # batches one swim watch runs at once
  notify: false                    # notifications and a tab progress bar
  notify_url: https://ntfy.sh/TOPIC   # notifications to ntfy, terminal or not
  notify_command: ./my-notifier.sh    # or run this instead
  runners: {ts: tsx}               # saved when swim asks
repos:
  swimlane:                        # this repo, every branch and worktree
    header_env: [GOFLAGS, GITHUB_REF_NAME, RUNNER_OS]
    term_grace: 30s
  swimlane/release:                # only on the release branch
    signing: required
```

`config.yml` is read loosely: a key swim doesn't read (a typo, a
removed setting) is ignored, and `swim doctor` names it with its line
(`config-key`). Personal env goes in [env files](env.md), not
config. There is no lane count. A `repos:` key that's a path (swim once
keyed repos by their git toplevel) matches nothing; `swim doctor` says
which to rename.

A CI runner has no config of its own, so `swim ci` runs on swim's
defaults. A lane that needs a value everywhere sets it itself (`export
X=...` in the script), or the CI job does (its `env:`).

## swim config

`swim config` prints the effective configuration: the repo's name and
branch, whether each has a section, and every setting with the layer it
comes from (`term_grace: 30s # repo`). `swim config KEY` prints one value;
`swim config KEY VALUE` sets it under `defaults:`, `--repo` sets it in
`repos.NAME`, and `--branch` in `repos.NAME/BRANCH`, keeping the file's
comments. `runners.EXT` goes to the repo's section unless `--branch` says
otherwise.

On a terminal, `swim config` (or `swim config edit`) is every setting on
one screen, swim watch's picker, with its value, the layer it comes from,
its default and what it takes, to change in place: Enter, Space or ← →
step a key's choices, Enter on another types its value on the bottom
line (checked before it's written), `d` removes the key from the
section. It edits `defaults:`, or with `--repo` / `--branch` that
section, and writes only what differs from what the section inherits, so
a key set back to that leaves the file. Its first write copies the file
as it was to `config.yml-N.bak`, one past the highest N there, like Nix's
profile generations; restore one with `cp`.

## Files

| Path | What | Commit? |
|---|---|---|
| `lane.N.sh` (`lane.N.<ext>`, `lane.N`) | Lane N's current round. | Only CI rounds. |
| `lane.N.yml` | A lane's header as YAML ([other languages](other-languages.md#header)). | With its script. |
| `.swim.log` | The project log. | No (or yes, to keep history) |
| `.env.swim` | Lane env. | No |
| `.swim/status.yml` | Every lane's state. | No |
| `.swim/logs/` | Lane logs, current and archived. | No |
| `.swim/chain.jsonl`, `.swim/scripts/` | The chain, and every script that ran. | No |
| `.swim/runs/<run>/<N>/`, `.swim/runs/<run>/<N>.manifest.yml`, `.swim/runs/RUN.yml` | Staged copies and manifests; each run's timeline. | No |
| `.swim/queue/`, `.swim/snapshots/` | Batches, snapshot output. | No |
| `.swim/laneN.pid` | Present while lane N runs. | No |

`swim init` sets these up and git-ignores what shouldn't be committed.
`swim doctor` checks what running lanes on this machine needs, and
`--fix` deletes what swim left behind (`swim doctor --help` lists both).
