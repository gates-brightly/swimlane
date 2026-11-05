# Tasks

Open work. Each task has been checked against the code. **Today** says what
exists, with file:line references. **Do** says what's missing. **Done when**
gives the acceptance check. When a task ships, delete it: git history keeps
the record, and the claim it backs belongs in the README,
[docs/reference/trust-model.md](../docs/reference/trust-model.md) or `swim guide`.

Newer tasks live one per file in [`tasks/`](tasks/), numbered after the
ones here.

Priorities:

- **P0**: the claim the product makes isn't backed by the code yet
- **P1**: needed before I can rely on it for real work
- **P2**: makes my own loop faster, safer or lighter
- **P3**: adoption by others (a bonus, not a goal)

Kinds (the audit's advice: keep these apart in issues and PRs):

- **bug**: a defect reproduced or confirmed in the code
- **gap**: a missing guarantee or design decision
- **hypothesis**: an attack scenario nobody has tested yet

**The rule for every task:** a feature must save time, reduce risk or
reduce cognitive load in your actual work. If it does none of these, it
doesn't get built, and an existing one that doesn't (T23 measures it) gets
cut.

**Where new work goes (from 2026-11-03, for a month):** the queue, the
dependency graph and the log the AI reads back, the loop that makes swim
faster for an operator. No new trust levels, anchors or verify modes until
then; T31–T33 are the candidates.

Keep this in mind throughout. Approval, signing and the chain prove **who**
decided **what**. They don't make an arbitrary script safe. Every task
should make that line sharper, not blur it.

| Task | Priority | Kind |
|---|---|---|
| [T21](#t21-repo-landing-page-p3) repo landing page | P3 | gap |
| [T23](#t23-friction-from-my-own-sessions-p1) friction from my own sessions, then cuts | P1 | gap |
| [T29](tasks/T29-ci-e2e-red-on-main.md) e2e red on main in CI | P0 | bug |
| [T30](tasks/T30-decision-checkpoint-followups.md) decisions and checkpoints: the unbuilt parts of their specs | P2 | gap |
| [T31](tasks/T31-watch-says-why-a-batch-waits.md) `swim watch` says why a batch waits | P2 | gap |
| [T32](tasks/T32-log-since-fail.md) `swim log N --failed` | P2 | gap |
| [T33](tasks/T33-requeue-from-the-failure.md) requeue a failed batch from where it stopped | P2 | gap |
| [T34](tasks/T34-runkey-test-flake.md) `TestRunKeyKeepsSigningFromLanes` is flaky (fixed and unskipped; macOS CI to confirm) | P1 | bug |
| [T35](tasks/T35-swimmer-says-its-platform.md) the swimmer says what platform lanes run on | P2 | gap |
| [T36](tasks/T36-lint-gnu-only-tools.md) lint warns on GNU-only tool forms | P2 | gap |

---

## Reducing adoption friction

### T21. Repo landing page (P3)

**Today:** the README has the CI badge, a quickstart and an architecture
diagram.

**Do:** add to the GitHub repo:
- a description and topics
- one terminal recording of `swim run` in the live view (`demos/` has
  scenes worth recording)

### T23. Friction from my own sessions (P1)

swim is built for its author's own work: an opinionated tool that makes
one engineer faster. Others using it is a bonus, so the evidence for what
to fix or cut comes from that engineer's sessions, not outside ones.

**Today:** `.swim.log` records every command swim refused as typed
(`usage`) and every lint error by code and by what ran it (`lint`), since
`3da314f`. Nothing reads them back across repos or weeks.

**Do:** for two weeks of real work, in every repo that uses swim, record
in `tasks/T23-friction.md`, per week:

- **Trips**, from each repo's `.swim.log`:
  ```sh
  # lint errors by code (a line's "x2" counts once: lint runs that hit it)
  grep -E '^\S+\s+\S+\s+lint\s' .swim.log | sed -E 's/.* (lane\.[^:]+): (.*) \(via [a-z]+\)$/\2/' \
    | tr ',' '\n' | sed -E 's/^ +//; s/ x[0-9]+$//' | sort | uniq -c | sort -rn
  # commands refused as typed
  grep -E '^\S+\s+\S+\s+usage\s' .swim.log | sed -E 's/^\S+\s+\S+\s+usage\s+//' | sort | uniq -c | sort -rn
  ```
- **Rounds before a pass:** per job, the `fail` lines before its `pass`.
- **Cost per accepted change:** for each change that shipped, the AI
  session's tokens (Claude Code's `/usage` or the session's cost) and the
  swim jobs it took.
- **What ran at all:** every `.swim.log` event and command by count, for
  the cut list below.

**Cut candidates**, each kept only if the two weeks used it or it caught
something:

| Candidate | Why it's on the list |
|---|---|
| `cache`, `cache restore\|save` | build-tool speed-ups, not ops safety |
| `earmark` (`report --earmark`) | bare `swim`, `swim log` and `status` cover catching up |
| approvals from others (`lookup.yml`, `signoff`) | one engineer approves their own work |
| `trust.yml`, chain anchors | proof for an auditor, where the operator is the only reader |

**Done when:** two weeks are recorded; every trip seen three times or more
has a fix or a written reason; and every cut candidate is kept with a
reason, or removed.
