# T30. Decisions and checkpoints: what their specs asked for that isn't built (P2, gap)

Source: the specs `features/decision.md` and `features/checkpoint.md`,
removed when both features were built (2026-10-29). Their design is in
`swim guide` (decide, `--checkpoint`), docs/usage.md (Decisions,
Checkpoints) and the CHANGELOG. These are the parts left out.

## Decisions

**Today:** `decide` asks every time it's called (internal/cli/decide.go:73);
the live view's pre-answers are for guards only (`newPreAnswers`,
internal/launcher/launcher.go:1200); lint counts choices
(`checkDecisionChoices`, internal/lint/checks.go:709) but doesn't read the
`case` that follows.

**Do:**
- The same decision asked twice in a round gets the first answer again,
  not a second question (the launcher keeps the round's answers by name).
- Pre-answer a decision in the live view: on a lane that hasn't asked yet,
  a number key answers its decision ahead, pinned to the manifest, as `y`
  and `n` pre-answer its guards.
- Lint warns when a `case $DECISION in` has no branch for one of the
  numbers a literal `decide` offers.

**Done when:** an e2e test asks one decision twice and sees one prompt; a
pty test pre-answers a decision in the live view; a lint test flags a
missing branch.

## Checkpoints

**Today:** `swim clean` archives and stubs a lane but leaves
`.swim/checkpoints/laneN.json` (internal/cli/clean.go); a new round ignores
it, so it's only clutter. No demo shows checkpoints.

**Do:**
- `swim clean` (and `swim stub`) remove the lane's checkpoint file.
- A demo scene: demo 2's worker lane fails mid-way, is fixed, and its rerun
  keeps the migration step.

**Done when:** an e2e test cleans a lane and finds no checkpoint file;
the demo runs with `DEMO_AUTO=1`.

## Open questions they left

- Signed decisions, or a `lookup.yml` rule that names one ("two DBAs pick
  the strategy"). Proposal was no: guard the branch instead.
- A checkpoint revalidated by a cheap check (`--checkpoint-if "aws s3 ls
  ..."`) rather than only by age (`--checkpoint=4h`).
- A warning when a step reads a file a kept step would have made (the
  staged copy is fresh each run).

Settle these with T23's sessions, if anyone needs them.
