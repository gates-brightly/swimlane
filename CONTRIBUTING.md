# Contributing

```sh
just test        # unit tests (fast)
just e2e         # the Go e2e suite, whole-workflow scenarios included
just check       # gofmt, vet, unit and e2e tests
just ci          # this repo's CI lanes (lane.1.sh lists them), through swim ci
just next-tag    # tag HEAD with the next release (DRY=1 prints it); see CHANGELOG.md's release steps
```

- **End-to-end:** `internal/e2e` drives the built binary one command at a
  time; its `TestScenario*` tests run whole lane DAGs offline
  ([e2e/README.md](e2e/README.md)).
- **Security:** `go test ./internal/e2e -run TestSecurity -v`, one subtest
  per audit case. A known gap skips with `gap: <what swim doesn't do
  yet>`, so the suite lists what's open. A `T<n>` in a test's comment
  names a task from the old `dev/tasks/` (removed in 3ff2cf1): `git show
  3ff2cf1^:dev/tasks.md` lists them.
- **Crashes:** `go test ./internal/e2e -run TestFault` builds swim with
  `-tags swimfailpoint` and kills it at the points `internal/failpoint`
  lists; `SWIM_FAILPOINT=<point>:kill|pause` tries one by hand.
- **Fuzzing:** `FuzzParse` (`internal/logparse`), `FuzzParseScript`
  (`internal/lane`), `FuzzDecodeLine` (`internal/chain`). Commit a crash
  the fuzzer finds (`testdata/fuzz`) with the fix.
- **State:** `SWIM_CHECK_STATES=1` (set by the e2e suite) panics on an
  illegal `status.yml` transition ([state](docs/reference/state.md)).
- **Bash 3.2:** `internal/assets/lib.sh` and the e2e lane scripts must run
  on macOS's bash.
- **Docs:** each topic is written once, in `docs/reference/` (the binary
  embeds it for `swim guide`). The README and `docs/guide.md` link there
  rather than repeat it; their commands run in the test suite.
- **Decisions:** one that shapes swim gets a record in
  [docs/adr/](docs/adr/README.md) (copy its template); read the record
  before undoing one.
- **Incompatible changes:** bump `syntax.Version` (`internal/syntax`) and
  write Upgrading notes in `CHANGELOG.md`.
