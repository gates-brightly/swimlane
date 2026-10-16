# A docs page and a quickstart that teaches swim in an afternoon

Status: proposed

## Summary

swim's documentation today is a long README, a 1,000+ line guide in the
binary (`swim --help`), and a few deep pages in `docs/` (architecture,
trust model, state, recovery, compatibility, SDK examples). It's complete
and accurate, and hard to start from: a newcomer meets lanes, jobs, cells,
rounds, batches, manifests, evidence, guards, the chain, the hub and
modules all at once.

This adds:

- **`docs/README.md`**: an index page. What swim is in a paragraph, then the
  docs in the order to read them, each with one line on what it's for.
- **`docs/quickstart.md`**: a guided first hour, from install to a guarded
  change, an approval, a queued batch and reading the record, with each
  concept introduced when the reader first needs it.
- **The README** keeps its five-minute quickstart and points to both.

## Motivation

- The learning curve is the biggest risk to adoption (the 2026-10-16
  review). T23 (outside feedback) needs a starting point other than the
  README and `swim --help`.
- The README's quickstart shows two harmless lanes. It doesn't show what
  swim is for: a guard and its dry run, an approval, what the log and
  `swim report` say, a module.
- `docs/` has no index: a reader finds `trust-model.md` or `state.md` only
  through links from the README.

## Behaviour

### `docs/README.md`

1. **What swim is**, in a paragraph: Claude (or a person) writes a round,
   the operator runs it with their access, the record says what happened.
2. **Start here:** the quickstart (below), then the README's usage patterns.
3. **Concepts**, one line each, linking to where they're explained: lanes and
   rounds, stages, guards and approvals, the execution manifest, evidence,
   modules, the queue and workers, the chain of custody.
4. **Reference:** `swim --help` (and how to extract one section), the
   command list, `docs/compatibility.md` (what's stable), the YAML schemas.
5. **Deep dives:** architecture, trust model, state, recovery.
6. **SDKs:** Python, TypeScript, Go (`docs/examples/`).

### `docs/quickstart.md`

A scratch repo, no cloud credentials, about an hour, in steps that each end
with something to look at:

1. **Install and init.** `go install ...`, `swim init`, what it wrote.
2. **A first round.** `swim new 1`, one harmless step, `swim run 1`, then
   `swim log 1` and `swim status`: what PASS means (exit 0, nothing more).
3. **Stages and a gate.** A check that fails closed; the round stops before
   change.
4. **A guard.** A "destructive" step (`rm` of a scratch file) behind
   `guard`; the dry run; approving at the prompt; the APPROVED line.
5. **Approve ahead, and what stales it.** `swim approve`; edit the script;
   the approval goes stale and says why.
6. **Two lanes and a dependency.** `After:`, `swim plan`, a failure that
   skips its dependent.
7. **Queue and a worker.** `swim queue add`, `swim watch --once`.
8. **A module.** `# Modules: wait report` with a local HTTP server, `mod
   wait http`, `mod report`.
9. **Reading the record.** `swim report --earmark`, `.swim.log`, `swim
   timeline`, `swim verify`.
10. **With Claude.** Install the skill (`swim skill`), and the
    planner/operator loop: Claude writes and queues, you approve and run.

Each step's commands are tested, like the README's quickstart blocks
(`internal/e2e/quickstart_test.go`), so the page can't rot.

## Design

| Area | Change |
|---|---|
| `docs/README.md`, `docs/quickstart.md` | New. Commands in fenced `sh` blocks between `<!-- quickstart -->` markers, one block per step. |
| `internal/e2e/quickstart_test.go` | Also runs `docs/quickstart.md`'s blocks, in order, in a scratch repo (the prompt step answered through the approval channel, as the PTY tests do). |
| `internal/docs` | Links from `docs/README.md` resolve; every `docs/*.md` is listed in it. |
| `README.md` | "Docs" near the top: the index and the quickstart. |

## Open questions

- **A rendered site** (GitHub Pages, mkdocs) or plain Markdown in the repo?
  Leaning: Markdown first; GitHub renders it, and the tests keep it true.
- **The interactive approval step** (step 4) needs a terminal. Test it
  through a PTY, as the approval tests do, or let the page approve with
  `FLAG=1` and say that the prompt is the usual way?
- **How much of the guide** moves into `docs/`? None, to start: the guide
  stays the reference, compiled into the binary; `docs/` teaches and
  links to it.

## Testing

- The quickstart's blocks run in CI (both OSes) and pass.
- Every link in `docs/README.md` resolves; every page in `docs/` is listed.
- T23's testers start from `docs/README.md`; what they trip on goes in T23.

## Out of scope

- Rewriting the guide or the README's reference sections.
- A marketing site, video or screencast (T21 covers the GitHub landing
  page and one recording).
