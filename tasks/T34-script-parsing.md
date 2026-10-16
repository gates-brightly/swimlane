# T34. Regexes left after the header contract (P2, gap)

Source: bugs found while building modules, 2026-10-16.

swim reads a bash lane script to know what it does before it runs: its
guards, stages, evidence steps, module calls, sourced files, handlers.
Approvals, the manifest and lint rest on that reading. Today it's
regular expressions, about 75 of them across `internal/lane` and
`internal/lint`, and two readers that disagree:

- **The launcher's** (`lane.ParseScript`, `internal/lane/lane.go`) matches
  each raw line. It decides the guards a lane may ask for (the manifest's
  `guards:`), whether it has stages (`Staged`) and evidence
  (`UsesEvidence`), which approvals then cover.
- **Lint's** (`internal/lint/shell.go`) scans the script first into views
  with comments, quoted text and heredoc bodies blanked, then matches.

## Seen

- A guard found in quoted text: `echo "no report: the guard was declined"`
  made `was` one of the lane's guards in `swim plan`, the manifest and
  approvals. Fixed for guards only (`blankQuoted`, `31a16a5`); lint's
  view already ignored it.
- Stage and evidence detection still match inside quotes: `echo "next:
  stage change"` sets `Staged`.
- A module call found by its word anywhere: the first `mod` regex took
  `go mod download` for a module call; fixed by matching command position
  only.
- Multi-line constructs (a `mod` call continued with `\`, a quoted string
  over several lines, a heredoc) are read line by line, so a call's
  arguments, or a quoted guard on its second line, can be missed or
  invented.
- Each fix is local to one regex, and the two readers drift apart.

## Decided (2026-10-16): the header is the contract

Not "which parser": take body reading out of the trust path.

- **Header only for what a lane may do.** `lane.ParseScript` reads guards
  (`# Guards:`, `*` for flags named at run time), includes and modules
  from the header (and sidecar) alone. The manifest pins only what the
  header names. At run time, the approval broker and the guard hook refuse
  an undeclared flag, and the library's `source` stops the round on a repo
  file the header doesn't list. `swim lint --fix` writes the lines.
- **The body only places things.** Stages, outcome handlers (and the
  guards inside them, for display) and evidence steps are found by a
  quote-aware tokenizer (`lane.Commands`, `internal/lane/text.go`); a miss
  errs safe (no stages: `swim plan --check` refuses the lane).
- **No regexes in `internal/lane` or `internal/syntax`;** names, numbers and
  slugs are checked with `internal/chars`. The static source scan moved to
  lint (`internal/lint/sources.go`): advice, not trust.

Count: about 170 regexp declarations before, 117 after.

## Left

- **Lint** (53 of the 117: `checks.go`, `sources.go`, `modules.go`,
  `cache.go`, `lint.go`, `shell.go`): rewrite its checks on `lane.Commands`
  and its views. Its findings are advice now, so this is about accuracy,
  not trust.
- **Log and text parsers:** `logparse` (9), `cli/logcmd.go` (7),
  `redact` (6, secret value patterns: likely to stay), `doctor` (5),
  `changelog` (5), `queue`, `schedule`, `hub`, `envfile` and others with
  one to three each.
- **A corpus test** of tricky scripts (quotes, heredocs, continued lines,
  `bash -c` bodies) for what the tokenizer and lint must find.

## Done when

The remaining regexes are gone or each kept for a reason recorded here
(redact's credential shapes, say), and the corpus test exists.
