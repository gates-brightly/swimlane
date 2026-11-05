# Config

Personal settings live outside the repo, so workers can't change them:
`~/.config/swim/config.yml` (`$XDG_CONFIG_HOME/swim/config.yml`). `swim
config [KEY [VALUE]]` shows or sets them, keeping comments. On a
terminal, `swim config` (or `swim config edit`) is every setting on one
screen, swim watch's picker, with its value, default and what it takes,
to change in place: Enter, Space or ← → step a key's choices, Enter on
another types its value on the bottom line (checked before it's
written), `d` goes back to the default. It writes only what differs from
the default, so a key set back to its default leaves the file. Piped, or
from an agent, `swim config` prints the effective configuration. Its first write copies the
file as it was to `config.yml-N.bak`, one past the highest N there, like
Nix's profile generations; restore one with `cp`.

```yaml
defaults:
  max_parallel: 8                  # lanes at once (0: unlimited, the default)
  blocked_commands: ["terraform destroy"]   # no lane command may contain these (none by default)
  secret_env: [GITHUB_TOKEN]       # masked as *** everywhere swim writes
  secret_env_auto: true            # also *_TOKEN, *_SECRET, *_PASSWORD, *_API_KEY, ...
  secret_env_ignore: [CSRF_TOKEN]  # exempt from secret_env_auto
  secret_patterns_auto: true       # also AWS key ids, GitHub/Slack tokens, JWTs
  header_env: [STAGE, AWS_PROFILE] # recorded in every step header (never secrets)
  runtime: node --version          # recorded as the runtime version
  toolchain: . "$HOME/.nvm/nvm.sh" && nvm use >/dev/null
  interrupt: graceful              # graceful | immediate: the first Ctrl-C
  interrupt_grace: 5s              # force quit: SIGINT, SIGKILL after this
  term_grace: 10s                  # SIGTERM: graceful, then force after this
  signing: auto                    # auto | required | off
  signing_key: ~/.ssh/id_ed25519   # a .pub signs through ssh-agent
  queue_lease: 2m                  # a swimmer's heartbeat, seen from other hosts
  claim_lease: 2h                  # how long a lane claim lasts
  watch_batches: 4                 # batches one swim watch runs at once
  notify: false                    # notifications and a tab progress bar
  notify_url: https://ntfy.sh/TOPIC   # notifications to ntfy, terminal or not; config.yml only
  notify_command: ./my-notifier.sh    # or run this instead; config.yml only
  runners: {ts: tsx}               # saved when swim asks
repos:
  /path/to/repo:                   # git toplevel; overrides defaults key by key
    max_parallel: 2
```

A repo may commit `swim.yml` at its root with the same keys as a `repos:`
entry: it applies over `defaults` and under your own entry, so a CI runner
and every operator share it. Unknown keys are an error. Personal env goes
in [env files](env.md), not config. There is no lane count.

## Files

| Path | What | Commit? |
|---|---|---|
| `lane.N.sh` (`lane.N.<ext>`, `lane.N`) | Lane N's current round. | Only CI rounds. |
| `lane.N.yml` | A lane's header as YAML ([other languages](other-languages.md#header)). | With its script. |
| `swim.yml` | The repo's shared settings. | Yes |
| `.swim.lock` | The repo's [syntax versions](compatibility.md#syntax-versions-and-the-lock). Never edit it to get past a mismatch. | Yes |
| `.swim.log` | The project log. | No (or yes, to keep history) |
| `.env.swim` | Lane env. | No |
| `.swim/status.yml` | Every lane's state. | No |
| `.swim/logs/` | Lane logs, current and archived. | No |
| `.swim/chain.jsonl`, `.swim/scripts/` | The chain, and every script that ran. | No (export it) |
| `.swim/runs/<run>/<N>/`, `.swim/runs/RUN.yml` | Staged copies and manifests; each run's timeline. | No |
| `.swim/queue/`, `.swim/snapshots/`, `.swim/signoffs/` | Batches, snapshot output, signoffs. | No |
| `.swim/laneN.pid` | Present while lane N runs. | No |

`swim init` sets these up (and git-ignores what shouldn't be committed);
`swim doctor [--fix]` checks the environment and repairs only swim's
`.gitignore` block and stale pid files.
