# Lane outputs: passing files between lanes

Status: proposed

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

## Open questions

1. **Size limit:** cap outputs per round (say 1 GB) to keep `.swim/` sane?
2. **Remote storage:** with the hub or a queue across machines, should
   outputs be uploaded somewhere shared? Out of scope for now: inputs are
   local to one machine.
3. **Passing small values:** a lighter `output KEY=value` for strings
   (an id, a version), readable as `$(input 2 KEY)`?
4. **Matrix inputs:** how does `input 3 stats.json` resolve when lane 3 is a
   matrix: all cells as a directory per cell, or `input '3[orders]' ...`?

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
