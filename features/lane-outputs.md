# Lane outputs: passing files between lanes

Status: shipped (unreleased)

## Summary

A round declares files it produces. Lanes that depend on it get their paths,
and the chain records their hashes:

```bash
# lane.2.sh
# Outputs: stats.json, pages/
run "compute stats" bash -c 'e2etool stats > "$SWIM_OUT/stats.json"'
```

```bash
# lane.4.sh
# After: 2 3
run "build report" e2etool report --stats "$(input 2 stats.json)" --pages "$(input 3 pages)"
```

## Motivation

- **Data passing is unofficial today.** Lanes in a DAG hand data to each
  other through ad hoc files in the repo or `/tmp`. Nothing says which lane
  wrote what. A rerun can read an older file, and two rounds can collide on
  one path.
- **Provenance:** for audit, "lane 4 built its report from *these* exact
  stats" should be provable, not assumed.

## Behaviour

### Producing

- **Where outputs go:** each round gets `$SWIM_OUT`, a fresh directory at
  `.swim/out/<job>/` (one per cell with
  [matrix-rounds.md](matrix-rounds.md)).
- **Declaring them:** `# Outputs:` lists files or directories, relative to
  `$SWIM_OUT`.
- **Checked at the end:** a declared output that's missing when the round
  ends adds `FAIL  declared output missing: stats.json`, and the round
  fails.
- **Undeclared files** in `$SWIM_OUT` are kept, but not hashed or offered to
  other lanes. They're reported with a `WARN`.

### Consuming

- **The lookup:** `input N <name>` (bash), or `swim input N <name>` in any
  language, prints the path of lane N's output from the round this run
  waited on.
- **Only declared dependencies:** a lane may only read outputs of lanes in
  its `After:`. Anything else is an error, so the dependency graph stays
  honest.
- **A job id pins one round:** `input 3f2a9c1e stats.json` reads the
  outputs of that exact round, even from an earlier run.
- **Read-only:** inputs are made read-only before the consuming lane
  starts.

### Records

- **The chain:**
  - **Producer:** a round entry gains `outputs: {name: sha256}`.
  - **Consumer:** its entry gains `inputs: {"2:stats.json": sha256}`.
  - **Verifying:** `swim verify` checks that each input hash matches the
    producer's recorded output.
- **The log:** the round's `END` block lists outputs with sizes and short
  hashes.
- **Archive and cleanup:** `swim archive` keeps a round's outputs with its
  log. Unreferenced outputs older than `outputs_keep` (default `14d`) are
  pruned by `swim doctor --fix`.
- **CI:** `swim ci` adds `.swim/out/` to the artifact list it prints.

### Safety

- **Not masked.** Outputs may be binary, so they aren't masked. Lint warns
  when an output's name suggests secrets (`*.env`, `*credentials*`), and
  docs say outputs must never hold secret values.
- **Not committed:** outputs live under `.swim/` and are git-ignored.

## Design

- **Header (`internal/lane/script.go`):** `Outputs []string`, validated as
  relative paths with no `..`.
- **Launcher:** creates `$SWIM_OUT` before exec and exports it. For each
  `After:` dependency it exports `SWIM_INPUT_<N>` as that round's output
  directory, which the `input` helper reads.
- **Finish (`_finish`):** checks declared outputs, hashes them (a
  directory hashes as a sorted manifest), and records them in status and
  the chain.
- **Chain (`internal/chain/chain.go`):** optional `outputs` and `inputs`
  maps, additive.
- **Queue:** a batch records the producer jobs its lanes will read from.

## Open questions (answered)

1. **Size limit:** no hard cap. A round whose outputs pass 1 GB gets a
   `WARN`, and `swim doctor --fix` prunes old outputs (`outputs_keep`,
   default 14d).
2. **Remote storage:** out of scope. Inputs are local to one machine.
3. **Small values:** yes. `output KEY value` records one, and
   `$(input 2 KEY)` prints it. Values are hashed into the chain as
   `value:KEY`.
4. **Matrix inputs:** settled with matrix rounds. Until then, a lane's
   outputs are its one round's.

Decided while building:

- **One directory per round:** `.swim/out/<job>/<run>/`, so a rerun of the
  same job gets a fresh `$SWIM_OUT` and older rounds stay addressable.
  `input JOB` reads that job's latest passed round, from status or the
  chain.
- **Read-only files:** outputs' files become read-only when the round
  ends. Directories stay writable, so the tree can still be pruned.
- **What's verified:** the consumer records the hash of each input as it
  read it. `swim verify` compares that hash with the producer's recorded
  output, so a file changed in between is caught.
- **Not done:** a queued batch doesn't pin the producer jobs its lanes will
  read. Today a lane reads its dependency's round from the same batch or
  run.
- **A failed `input`** records `FAIL  input N name (why)` (a failure) and
  prints nothing, so the step that used it fails too.

## Testing

- **Producing:** declared outputs hashed and recorded; a missing declared
  output fails the round; undeclared files warned about.
- **Consuming:** `input` resolves to the round the run waited on; reading a
  lane outside `After:` is an error; a job id pins an older round; inputs
  are read-only.
- **Chain:** output and input hashes recorded, and `swim verify` catches an
  input changed after it was produced.
- **Reruns:** a rerun gets a fresh `$SWIM_OUT`. Older rounds' outputs stay
  addressable by job id.
- **Non-bash:** `swim input` works from a Python lane.

## Out of scope

- **Sharing outputs across machines.**
- **Outputs as a general artifact store or cache.**
