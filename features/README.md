# features

Optional features, written down for implementation later. Nothing here is
built or promised. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| `swim ci`: a CI/CD mode (GitHub Actions, GitLab, others) with verbose logs, optimized for running on commits | [ci.md](ci.md) | shipped (4.20261009) |
| Interactive TUI for `swim all` / `swim run`: arrow keys pick a lane, up/down scroll its log, ESC returns to all lanes | [tui.md](tui.md) | shipped (4.20261009) |
| Blocked commands: swim never pushes, commits or pulls; a lane whose command contains a blocked substring is stopped | [blocked-commands.md](blocked-commands.md) | shipped (4.20261009) |
| Run id: one id per `swim run`, exported as `SWIM_RUN` and recorded in logs, status and `.swim.log` | [run-id.md](run-id.md) | shipped (4.20261009) |
| YAML output: `swim run --yaml` event stream, plus `--yaml` for `log`, `plan` and `status`, with versioned schemas | [yaml-output.md](yaml-output.md) | shipped (4.20261009) |
| Parallel limit: `max_parallel` / `--parallel N`, queueing ready lanes so the longest chain runs first | [parallel-limit.md](parallel-limit.md) | shipped (4.20261009) |
| Per-step timeouts and retries: `run --timeout 2m --retry 3 --backoff 5s`, on top of the round's `Timeout:` | [step-timeout-retry.md](step-timeout-retry.md) | shipped (4.20261009) |
| `swim lint` (lane scripts) and `swim doctor` (binary, PATH, config, `.gitignore`) | [lint-doctor.md](lint-doctor.md) | shipped (4.20261009) |
| Resource locks: `# Locks: name`, so lanes that share a resource never overlap, without ordering them or spreading failures | [resource-locks.md](resource-locks.md) | shipped (4.20261009) |
| Masking secrets: values of `secret_env` and auto-detected secret vars become `***` in logs, snapshots, status and output | [secret-masking.md](secret-masking.md) | shipped (4.20261009) |
| `swim timeline`: Gantt view of any run, with each lane's start delay, the longest chain and wait breakdown | [timeline.md](timeline.md) | shipped (4.20261009) |
| Chime: bell, sound or desktop notification when `swim run`/`swim all` finishes; `swim config chime true` | [chime.md](chime.md) | shipped (4.20261009) |
| Graceful Ctrl-C: first press stops every lane at the next step boundary, second force quits (SIGINT then SIGKILL), third kills | [graceful-interrupt.md](graceful-interrupt.md) | shipped (4.20261009) |

## Staging a feature

1. Add `features/<name>.md` using the sections in the existing specs:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, set the status to `shipped (<version>)`. Leave the spec in place
   as the design record. The user-facing docs are `swim --help` (`internal/help/guide.txt`) and the README.

Statuses: `proposed`, `accepted`, `in progress`, `shipped (<version>)`, `dropped (<why>)`.
