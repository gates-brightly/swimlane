# T34. Reconsider reading lane scripts with regexes (P2, gap)

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

## Do

Decide, and record the decision here, between:

1. **One scanner, then regexes.** Every reader (the launcher's too) works
   on lint's views (code without comments; bare without quoted text and
   heredocs), with continued lines joined. Smallest change; still regexes
   for commands.
2. **A real shell parser.** `mvdan.cc/sh/v3/syntax` parses bash (3.2's
   features included) into an AST: commands, their words, `if`, `&&`,
   functions, heredocs. Guards, stages, `mod` calls and `source` lines
   become call expressions with literal arguments; anything built at run
   time is visible as such. A new dependency, and parse errors to handle
   (a script that doesn't parse still runs).
3. **Keep regexes,** with the quote-blanking applied everywhere and a
   corpus test.

Whichever: a **corpus** of tricky scripts (quotes, heredocs, continued
lines, nested `if`, `bash -c '...'` bodies, functions, comments that look
like code), with what each reader must find, run against the launcher's
reader and lint's, so they can't disagree.

## Done when

The decision and its reasons are recorded here, the corpus test exists,
and the launcher and lint read every script in it the same way.
