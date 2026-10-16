# Tasks

Open work. Each task has been checked against the code. **Today** says what
exists, with file:line references. **Do** says what's missing. **Done when**
gives the acceptance check. When a task ships, delete it: git history keeps
the record, and the claim it backs belongs in the README,
[docs/trust-model.md](docs/trust-model.md) or `swim --help`.

Newer tasks live one per file in [`tasks/`](tasks/), numbered after the
ones here. Larger designs go in [`features/`](features/README.md).

Priorities:

- **P0**: the claim the product makes isn't backed by the code yet
- **P1**: needed before teams rely on it
- **P2**: adoption

Kinds (the audit's advice: keep these apart in issues and PRs):

- **bug**: a defect reproduced or confirmed in the code
- **gap**: a missing guarantee or design decision
- **hypothesis**: an attack scenario nobody has tested yet

Keep this in mind throughout. Approval, signing and the chain prove **who**
decided **what**. They don't make an arbitrary script safe. Every task
should make that line sharper, not blur it.

| Task | Priority | Kind |
|---|---|---|
| [T13](#t13-separate-the-parts-that-hold-guarantees-p2-maintainability) plan decisions as data (scheduler and guard routing done) | P2 | gap |
| [T18](#t18-add-a-license-p2) LICENSE | P2 | gap |
| [T21](#t21-repo-landing-page-p2) repo landing page | P2 | gap |
| [T23](#t23-outside-feedback-p2) outside feedback | P2 | gap |
| [T29](tasks/T29-ci-e2e-red-on-main.md) e2e red on main in CI | P0 | bug |
| [T33](tasks/T33-failures-outside-the-record.md) failures that don't reach the record | P1 | gap |
| [T34](tasks/T34-script-parsing.md) regexes left after the header contract (lint, log parsers) | P2 | gap |

---

## Making it operationally dependable

### T13. Separate the parts that hold guarantees (P2, maintainability)

**Today:** the scheduler's dependency decisions are pure: `depWaiter.await`
and `depVerdict` (`internal/launcher/sched.go`) take the run's state as
functions, and `runLane` uses them. Their property test
(`internal/launcher/sched_test.go`) checks hundreds of random graphs in
memory, on a logical clock, with `After:`, `Finally:` and locks; the e2e
one checks three end to end. `selectedCycle` has a property test of its
own. Who decides a guard is `guardRoute` (`internal/cli/approve.go`),
tested for every way a lane can be started.

**Do:** the same for `computePlan` (`internal/launcher/plan.go`): what
`swim plan` and `swim run` decide per lane (run, retry, rerun, skip,
always, held) from scripts, status and the queue, taken as data rather
than read from the repo, so plan and run can be checked to agree.

**Done when:** a property test checks plan decisions without a temp dir.

---

## Reducing adoption friction

### T18. Add a LICENSE (P2)

There's no LICENSE file. Pick one, then add it to the README and the GitHub
About section.

### T21. Repo landing page (P2)

**Today:** the README has the CI badge, a quickstart and an architecture
diagram.

**Do:** add to the GitHub repo:
- a description and topics
- a license badge (after T18)
- one terminal recording of `swim run` in the live view (`demos/` has
  scenes worth recording)

### T23. Outside feedback (P2)

Have two engineers who didn't build swim run a real change with it, starting
from the README quickstart and `demos/`. Record the friction here.
