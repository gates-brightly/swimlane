# Masking secrets in logs

Status: shipped (4.20261009)

## Summary

Swim replaces the values of configured secret environment variables with
`***` before writing anything: logs, snapshots, `status.yml`, `.swim.log` and
the copy of output it streams to the terminal. It does this through a
`secret_env` config list plus automatic detection of common secret variable
names.

```yaml
defaults:
  secret_env: [GITHUB_TOKEN, DATADOG_API_KEY]
  secret_env_auto: true      # also *_TOKEN, *_SECRET, *_PASSWORD, *_API_KEY, AWS_SECRET_ACCESS_KEY, ...
```

## Motivation

- **The rule isn't enforced:** the safety rules say "logs never contain secret
  values; print keys or lengths only", and "never list a secret in
  header_env". Nothing stops either from happening. One `env | sort` in a
  diagnostic step, or a CLI that echoes its config, puts a token into
  `.swim/logs/agentN.log`.
- **Logs travel:** they're kept forever (archived, not deleted). With
  [ci.md](ci.md) they're uploaded as CI artifacts, and `.swim.log` is meant to
  be committed.
- **AI sessions read the logs:** a secret in a log becomes a secret in a
  transcript.

## Behaviour

### Which values count as secrets

- every variable named in `secret_env`
- with `secret_env_auto`, which defaults to true, any variable whose name
  matches the built-in patterns: `*_TOKEN`, `*_SECRET`, `*_SECRET_*`,
  `*_PASSWORD`, `*_PASSWD`, `*_API_KEY`, `*_PRIVATE_KEY`,
  `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`
- values shorter than 6 characters are skipped, to avoid masking `1`, `true`
  and the like

`secret_env_ignore` lists names to exempt from the automatic patterns.

### Where masking happens

| where | what |
|---|---|
| `agentN.log` | step output, command lines (`$ …`), env header values |
| `.swim/snapshots/*` | snapshot output |
| terminal stream | the copy swim streams |
| `status.yml` and `.swim.log` | failed-step text, notes |
| `header_env` | a secret variable listed there is logged as `NAME=***(len 40)` and makes `swim doctor` warn |

Masking replaces each occurrence of a value with `***`. It also catches the
base64 and URL-encoded forms of each value, which are common in headers and
URLs.

### What masking doesn't do

- **Pipes and files:** output a step pipes elsewhere or writes to a file is
  not masked.
- **Transformed values:** values that are split or otherwise transformed
  aren't caught.
- **It isn't permission:** the rule "don't print secrets" stays. Masking is a
  backstop, and the guide should say exactly that.

Each round's header records `masked: 3 vars` (names only), so a reader knows
masking was active.

## Design

- **Masker** (`internal/step`, or a new `internal/redact`): `New(env, cfg)`
  collects secret values and returns a streaming replacer. A naive
  line-by-line replace misses values that span a buffer boundary, so the
  writer keeps a carry buffer as long as the longest secret minus one. It
  wraps every writer `swim step` uses: log, snapshot and stdout.
- **Env collection:** `swim step` runs in the lane's environment, so it sees
  the same variables the command will get. The names come from config,
  exported by `lane_init` as `SWIM_SECRET_ENV` and `SWIM_SECRET_AUTO`.
- **Status and history:** the strings come from logs, so they're already
  masked. `swim note` text is masked too, using the caller's environment.

## Decisions

1. **The terminal stream is masked too**, not only what's persisted: `swim
   step`'s stdout/stderr copies and the launcher's relay of lane output.
2. **Value patterns ship**, on by default behind `secret_patterns_auto`: AWS
   access key ids, GitHub classic and fine-grained tokens, Slack tokens, JWTs.
3. **No re-masking of old logs.** For logs written before this feature, a
   one-off `sed -i 's/<value>/***/g' .swim/logs/*.log` does it.
4. **Masking is line-buffered** (a line ends at `\n` or `\r`), which catches
   values split across writes and lets the patterns apply; output without a
   newline appears when its line ends (or when the step ends). Partial lines
   over 64KiB are flushed as they are.
5. **Where it's configured:** `swim step` and the hooks load config
   themselves, so no `SWIM_SECRET_ENV` export is needed. Lists
   (`secret_env`, `secret_env_ignore`) add up across defaults and the repo;
   the switches override.
6. **Multi-line values** (PEM keys) are also masked line by line.
7. The `swim doctor` warning for a secret listed in `header_env` lands with
   `lint-doctor.md`; until then the value is logged as `NAME=***(len N)`.
8. The spec's `secrets` scenario is covered by the Go e2e test
   `TestSecretMasking`: output, a snapshot (raw and base64), a failed step's
   label, a guard reason, `header_env` and `swim note`, then a scan of every
   file under `.swim/` and `.swim.log` for the raw values.

## Testing

- **Unit:**
  - the replacer: a value split across writes; overlapping values; base64 and
    URL-encoded forms; values shorter than the minimum are left alone
  - the auto patterns match the intended names and leave e.g.
    `TOKEN_COUNT=5` (too short) alone
- **Go e2e:** a lane with `GITHUB_TOKEN=ghp_testvalue123456` running
  `bash -c 'echo $GITHUB_TOKEN; env'`:
  - the value appears nowhere under `.swim/` or in `.swim.log`, and the
    stdout copy shows `***`
  - the round header says `masked: 1 vars`
- **Scenario:** `secrets`: lanes print known fake secrets through every path
  (output, a failed step's text, a snapshot, `header_env`, `swim note`). The
  check greps the whole scratch repo for the raw values and expects zero hits.
