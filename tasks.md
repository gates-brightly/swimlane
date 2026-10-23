# Tasks

Open work. Each task has been checked against the code. **Today** says what
exists, with file:line references. **Do** says what's missing. **Done when**
gives the acceptance check. When a task ships, delete it: git history keeps
the record, and the claim it backs belongs in the README,
[docs/trust-model.md](docs/trust-model.md) or `swim guide`.

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
| [T21](#t21-repo-landing-page-p2) repo landing page | P2 | gap |
| [T23](#t23-outside-feedback-p2) outside feedback | P2 | gap |
| [T29](tasks/T29-ci-e2e-red-on-main.md) e2e red on main in CI | P0 | bug |

---

## Reducing adoption friction

### T21. Repo landing page (P2)

**Today:** the README has the CI badge, a quickstart and an architecture
diagram.

**Do:** add to the GitHub repo:
- a description and topics
- one terminal recording of `swim run` in the live view (`demos/` has
  scenes worth recording)

### T23. Outside feedback (P2)

Have two engineers who didn't build swim run a real change with it, starting
from the README quickstart and `demos/`. Record the friction here.
