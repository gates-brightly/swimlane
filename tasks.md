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
| [T23](#t23-outside-feedback-p1) outside feedback, then cuts | P1 | gap |
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

### T23. Outside feedback (P1)

**Today:** nobody who didn't build swim has run a change with it. Every
command, concept and page was added without one. New features are frozen
until this is done ([features/](features/README.md)).

**Do:** five engineers who didn't build swim, each in a 60-minute session,
screen shared, without help unless they're stuck for five minutes:

1. **Cold start (15 min):** from the README only, install swim and finish
   the quickstart. Note where they stop, what they reread, and the first
   thing they get wrong.
2. **A real change (35 min):** with their own AI session, a small change
   from their backlog, one guarded step. The AI writes the lanes (`swim
   guide`); they review, approve and run them. A teammate co-approves if one
   is there.
3. **Debrief (10 min):** what they'd tell a colleague swim is, in one
   sentence; what they'd use again; what they'd cut.

Record per session, in `tasks/T23-sessions.md`: time to the first PASS,
every point they got stuck (the command and what they expected), the
commands they ran (`.swim.log` has them), the concepts they asked about,
and their one sentence.

**Cut candidates** to settle with the sessions. Each one stays only if a
session needed it or someone asks for it:

| Candidate | Why it's on the list |
|---|---|
| `chime`, `chime_style`, `chime_min_s` | sound notifications; not part of the core workflow |
| `schedule` | `swim queue` and cron cover it |
| `pause` / `unpause` | `interrupt` covers stopping; pausing is rare |
| `observe` (and observations in `report`, `status`) | useful later; another concept on day one |
| `cache`, `cache restore\|save` | build-tool speed-ups, not ops safety |
| hub users and roles (`auth users`) | a team feature before any team uses it |
| `secret grant` | swimmer secrets before anyone runs a swimmer |
| `earmark` (`report --earmark`) | `swim log` and `status` cover catching up |

**Done when:** five sessions are recorded, every cut candidate is decided
(kept with a reason, or removed), and the README's first screen and
quickstart are rewritten around what the sessions got stuck on.
