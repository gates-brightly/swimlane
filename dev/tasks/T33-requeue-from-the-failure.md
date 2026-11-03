# T33. Requeue a failed batch from where it stopped (P2, gap)

Source: the UX review (item 9).

**Today:** `swim queue requeue ID` (internal/cli/queue.go:697) puts a done
batch back as it was: every lane, pinned to the jobs and scripts it was
queued with (`queue.Requeue`, internal/queue/queue.go:592). Lanes that
passed run again, and a lane whose script was fixed since is skipped as
changed. Checkpointed steps (`run --checkpoint`) already skip on a lane's
next run.

**Do:**
- `swim queue requeue ID --failed`: a new batch of the lanes that didn't
  pass (failed, interrupted, or skipped because one failed), pinned to the
  scripts as they are now (the fix), carrying the batch's approvals only
  for lanes whose manifest hasn't changed. Lanes that passed are left out,
  and `# After:` on a lane that passed counts as met.
- The watch screen: on a finished batch, a key requeues it this way.

**Done when:** an e2e test runs a two-lane batch where lane 2 (`After: 1`)
fails, fixes lane 2, requeues with `--failed`, and only lane 2 runs, with
its old approval refused if its manifest changed.
