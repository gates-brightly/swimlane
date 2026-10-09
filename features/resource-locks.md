# Resource locks between lanes

Status: proposed

## Summary

A lane can declare named resources it needs exclusive use of:

```bash
# Locks: orders-table, vpc-prod
```

Two lanes that share a lock never run at the same time, but neither has to
wait for the other to pass. Whichever gets the lock first runs, and the other
starts once it finishes. If the first fails, the second still runs.

## Motivation

- **Ordering is the only tool today:** two rounds that touch the same thing
  (a table migration and a backup of that table; two Terraform applies against
  one state) must not overlap. A planner's only option is `# After:`, which
  means two things that aren't wanted here:
  - **a fixed order**, when either order would be fine
  - **failure spreading**: if the first lane fails, the second is skipped
- **There's no shared view:** workers are told to "use names no other session
  will reuse" and never touch each other's resources. A lock makes the
  remaining shared resources explicit.

## Behaviour

### Declaring and acquiring

- **Header:** `# Locks: a, b` lists one or more names. Names are letters,
  digits, `.`, `_`, `-` and `/`, so paths like `tf/orders` read naturally.
- **Order of events:** a lane first waits for its dependencies to pass, then
  acquires all its locks, then starts.
- **All at once:** locks are taken as a set (all or none), so lanes that each
  hold one lock can't deadlock waiting for each other's.
- **Fairness:** waiters are served in the order they became ready. A lane that
  needs two locks doesn't starve behind a stream of lanes needing one.
- **What it looks like:**
  - the panel shows `locked (orders-table: swim 4)`
  - plain mode prints `[7] waiting for lock orders-table (held by swim 4)`
  - the YAML stream adds `lock_wait` and `lock_acquired` events
- **Release:** locks are released when the lane finishes, whether it passed,
  failed or was interrupted. A failure doesn't spread through a lock.
- **Parallel limit:** with [parallel-limit.md](parallel-limit.md), a lane takes
  a slot only after it gets its locks, so a lane waiting for a lock doesn't
  waste a slot.

### Scope

Locks apply within one `swim run`, and across concurrent `swim run`s in the
same repo, as when two operators or terminals run different lanes at once.
Cross-run locks are files, `.swim/locks/<name>`, held with `flock` semantics.
They hold the holder's lane, job, run id and pid, and are released by the
process ending, so a killed run never leaves a stale lock.

`swim status` shows held locks. `swim plan` shows which lanes contend for
which locks.

## Design

- **Header:** `lane.Info.Locks []string` parsed from `# Locks:` (in
  `internal/lane`). Lint reports invalid names.
- **Within a run:** the launcher keeps a lock table (name → holder, wait
  queue) guarded by the same mutex as scheduling. A lane goroutine blocks on
  "all my locks free" after its dependencies, the same way it blocks on
  dependencies today.
- **Across runs:** an OS lock (`syscall.Flock`, `LOCK_EX|LOCK_NB` with
  polling, or blocking in a goroutine) on `.swim/locks/<name>.lock`, taken by
  the launcher for each lane before exec. The file body is informational.
- **Cycles:** taking all locks as a set and in name order avoids deadlock.
  `# After:` and locks can't create a wait cycle, because locks are acquired
  only once dependencies are met.

## Open questions

1. **Shared (reader) locks?** `# Locks: orders-table:read` would let readers
   share a resource while writers get it alone. Useful for snapshot-only
   rounds, but it adds complexity. Recommend exclusive only to start.
2. **Locks across machines?** Two operators on different laptops against the
   same AWS account aren't protected. Out of scope, and it should be
   documented so nobody assumes otherwise.
3. **Merge with parallel limits?** A lock is a pool of size 1, so
   `# Pool: aws` with `pools: {aws: 4}` generalises both. Decide before
   implementing either.

## Testing

- **Unit:** lock-table logic: all-at-once acquisition, FIFO fairness, release
  on every outcome.
- **Go e2e:**
  - two lanes with the same lock, each `sleep 1`, with no dependency: no
    overlap (check start and end times), total about 2s, both run even if the
    first fails
  - three lanes over locks {a}, {a, b} and {b}: no deadlock, and no lane
    shares a lock with a lane running at the same time
  - two concurrent `swim run` processes in one repo respect the same lock
  - a SIGKILLed run releases its lock
- **Scenario:** `locks`: dag99-style generated lanes with random locks from a
  pool of five names. The audit checks that no two lanes sharing a lock
  overlapped, and that lock waits never caused a lane to be skipped.
