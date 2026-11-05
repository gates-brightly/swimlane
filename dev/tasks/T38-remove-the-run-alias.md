# T38. Remove the run alias (P2, gap)

Source: the rename of a lane's step function from `run` to `step`. `run`
was kept for one release as a deprecated alias so lanes in other repos
keep working until their authors run `swim lint --fix`.

**Today:** the alias is in four places, each marked "Remove after the next
release (T38)":
- `run()` in internal/assets/lib.sh, beside `step()`.
- In-lane `swim run "<label>" -- cmd`: `cmdRun`'s `hasDashDash` branch
  (internal/cli/commands.go) and `hasDashDash` itself
  (internal/cli/lanecmds.go).
- Lint: `run` in `stepFuncs` and the `step|run|gate|snapshot` patterns
  (internal/lint/steps.go), the `run-is-step` check (`checkRunIsStep`,
  internal/lint/lint.go's table) and `swim lint --fix`'s rename
  (`runCalls`, `renameRun`, `Fixed.Renamed` in internal/lint/fix.go;
  its report in internal/cli/lint.go).
- Docs: lanes.md's `step` row, formats.md's lane-argument order, `swim
  step --help`.

**Do:** once a release with `step` has shipped, delete all of it, with
TestRenameRun, TestRunIsStep (internal/lint/rename_test.go) and
TestRunAliasForStep (internal/e2e/runtimes_test.go). A lane that still
calls `run` then fails as "command not found", which `_swim_on_err` logs.

**Done when:** `git grep -n T38` finds only dev/tasks.md and this file, and
lanes 1, 8, 9 and 2 pass.
