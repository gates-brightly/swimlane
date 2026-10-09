# Limit on parallel lanes

Status: shipped (unreleased)

## Summary

Cap how many lanes run at once. A lane whose dependencies are met but has no
free slot waits in a queue and shows `queued` until one frees up. The default
is unlimited, which is today's behaviour.

```sh
swim all --parallel 8
```
```yaml
defaults:
  max_parallel: 8          # per repo override under repos:
```

## Motivation

- **Real calls hit limits:** the dag99 scenario starts every ready lane at once.
  At 99 lanes that's fine for sleeps, but real rounds call AWS, Terraform and
  kubectl. Twenty lanes listing CloudFormation stacks at the same moment hit
  API rate limits, exhaust SSO token refreshes and saturate the operator's
  VPN.
- **Contention showed even in the scenario:** with 0–10ms of work, scheduler
  latency went up as more lanes ran at once (p95 0.09s → 0.17s), all from
  process start-up contention.
- **No way to throttle today:** an operator can only invent dependencies to
  slow things down, and that changes failure behaviour, because dependents get
  skipped.

## Behaviour

- **How the cap applies:** it counts running lanes. Waiting (on dependencies)
  and queued lanes don't count.
- **Who gets a free slot:**
  - the ready lane with the longest chain still below it, which keeps the
    critical path moving
  - then the lowest lane number

  The run should take no longer than it would unlimited, wherever the cap
  allows.
- **What it looks like:**
  - the panel shows `queued` (with position) and the header reads
    `running 8/8 · queued 14`
  - plain mode prints `[23] queued (14 ahead)` once
  - the YAML stream adds a `queued` event
- **Where it's set:** `--parallel N` overrides config. `--parallel 0` means
  unlimited.
- **Dependencies first:** a lane is queued only after its dependencies have
  passed. Skips still spread through the graph immediately.

## Design

- **Scheduler:** the launcher already runs a goroutine per lane that blocks
  until its dependencies finish (`runLane`). Add a counting semaphore
  (`chan struct{}` of size N) that a lane acquires after its dependencies and
  before `exec`, and releases in `finish`.
- **Priority:** priority needs a small ready queue, not a bare semaphore: a
  mutex-guarded heap ordered by (longest chain below, lane number). The
  chain lengths are computed once from `ResolveDeps`.
- **Config:** `Config.MaxParallel int` (`max_parallel`), validated `>= 0`. It
  is recorded in the `run` event of `.swim.log`.

## Decisions

1. **Global cap only.** Pools (`# Pool: aws`) are not built; resource locks
   ship as exclusive locks. Revisit pools if both prove too coarse.
2. **Ctrl-C:** queued lanes count as **skipped** ("interrupted before start"),
   the same as lanes that hadn't started.
3. **Config:** `max_parallel` is a pointer in config so a repo section can set
   `0` (unlimited) over a non-zero default. The cap is recorded in the `run`
   event (`max_parallel=N`), and each queued lane gets a `queued` event in
   `.swim.log` and `state: queued` in status.yml.
4. **Display:** the existing pre-start state was renamed `starting`; `queued`
   now means "waiting for a slot", shown as `queued (#n)` in the panel and
   `queued (n ahead)` once in plain mode.
5. **Audit:** dag99's audit reports peak concurrency for every run, and fails
   it when `E2E_MAX_PARALLEL` is exceeded (the `dag99-parallel` scenario).

## Testing

- **Unit:** the priority order, given a graph.
- **Go e2e:** with ten independent lanes, each `sleep 1`, and `--parallel 3`:
  - at most 3 have pid files at any moment (sample `status.yml`)
  - the total time is about 4s
  - all pass
- **Scenario:** `dag99-parallel`, which is dag99 with `--parallel 8`. The
  audit gains a check that no more than 8 lanes overlapped at any instant,
  computed from the nodes' start and end times. Every existing edge and value
  check still holds.
