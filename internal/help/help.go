// Package help holds swim's user and agent documentation: the full guide
// printed by `swim --help` and per-command detail.
package help

import (
	_ "embed"
	"sort"
)

//go:embed guide.txt
var Guide string

// Commands maps each command to its detailed help.
var Commands = map[string]string{
	"init": `swim init

  Set up the current repo (run from anywhere inside it):
    - creates ~/.config/swim/config.yml if missing and adds a section for this
      repo (keyed by git toplevel) with lanes and an empty deps table;
      existing content and comments are kept
    - adds a marked block to .gitignore: lane.[0-9]*.sh, .lane*.rc, .swim/,
      .swim.log
    - creates .swim/status.yml with every lane idle
    - records "init" in .swim.log, the project log (git-ignored too; drop it
      from the block if you want to commit the project's history)
  Safe to run again.
`,
	"config": `swim config [--path] [--lanes N]

  Print the effective configuration for this repo (defaults merged with the
  repo's section), or with --path just the config file location.
  --lanes N   set this repo's lane count (swim 1..N) in the config file,
              keeping its comments; recorded in .swim.log. Refuses to drop a
              lane that holds a pending round.
  Config lives outside the repo, in ~/.config/swim/config.yml
  ($XDG_CONFIG_HOME/swim/config.yml if set). Workers must not edit it; give
  the operator exact YAML lines instead.
`,
	"new": `swim new N "<round goal>" [--job ID] [--force]

  Write lane.N.sh from the template: the goal on its Round: line, a new job
  id on its Job: line, the toolchain preamble from config, and example
  snapshot/gate/guard/verify steps to replace. Prints the job id. Marks the
  lane's pending round and job in status.yml.
  Refuses if lane N is running, or already holds a pending round (use
  --force to replace a pending round you own).
  --job ID   use your own job id instead of a random UUID: 8-64 letters,
             digits, '.', '_' or '-', not all digits, unique across lanes.
`,
	"run": `swim run [N|JOB ...] [--rerun] [--plain]

  Run lanes. Each argument is a lane number or a job id (full, or 8+
  characters of it). A job id pins the run to that job: it runs the lane
  whose script holds it, and refuses if none does. Named lanes always run,
  even if their round already passed (rounds are rerunnable).
  With no arguments (or "swim all"), runs every lane whose lane.N.sh holds a
  pending round (a Round: line, not a stub) that hasn't passed yet; rounds
  that passed show as "done". --rerun runs them too.
  Lanes run in parallel as "bash lane.N.sh" from the repo root.
    - The top of the screen pins one row per lane (swim 1..N): its state and
      round. Lanes waiting on a dependency show a throbber and
      "waiting on swim N". Output scrolls below, prefixed [N].
      The panel shows at most 15 lanes. With more, a "… N more" line counts
      the hidden ones; idle, done and passed lanes are hidden first, then
      waiting and failed ones, so running lanes stay visible.
    - Dependencies come from each script's "# After:" line and config deps.
      A lane starts when its dependencies in this run pass, and is skipped if
      one fails or is skipped, or if a dependency outside the run holds a
      round that hasn't passed. Cycles stop the run before it starts.
    - With more than one lane, lanes get no stdin (prompts fail closed).
    - Ctrl-C reaches running lanes; lanes not yet started are skipped; the
      summary still prints.
    - Ends with a per-lane summary: result, exit code, counts, failed steps.
  Exits 0 only if every lane passed.
  Shorthand: "swim 1 2" is "swim run 1 2"; "swim 3f2a9c1e" is "swim run 3f2a9c1e".
  --plain   no pinned panel, colour or throbber; state changes print as lines.
            Automatic when stdout isn't a terminal or NO_COLOR is set.
  Guard flags are passed through the environment:
    FIN_ALLOW_DELETE_ZG_ITEMS=1 swim run 2
`,
	"all": `swim all [--rerun] [--plain]

  Run every lane whose pending round hasn't passed yet: the same as
  "swim run" with no lane numbers. Rounds that already passed are shown as
  "done" and not run again; failed, interrupted, skipped and new rounds run.
  --rerun   run every pending round, including ones that already passed.
  If lane scripts exist beyond the configured lanes (lane.5.sh with
  lanes: 4) holding jobs that haven't passed, swim asks whether to raise the
  lane count so they run. Without a terminal it never asks or changes
  config; it prints the "swim config --lanes N" command instead. Dependencies ("# After:" lines and config deps), the live
  view, guard flags and the summary all work as in "swim run".
  Stubbed lanes and lanes without a Round: line are left alone.
`,
	"plan": `swim plan [N|JOB ...] [--rerun]

  Show what "swim all" (or "swim run N ...", with lanes) would do, without
  running or changing anything, Terraform style:
      [+]    run    a new job
      [~]    retry  the job ran before and didn't pass
      [+/-]  rerun  the job already passed and runs again (named lane, --rerun)
      [-]    skip   the job won't run (a dependency outside the run hasn't
                    passed, or one in the run won't run)
  The jobs form a tree: top-level lanes start at once, and each lane sits
  under the lanes it waits for ("# After:" and config deps); a lane waiting
  for several shows "wait [2,3]" and appears in full once,
  "(shown above)" elsewhere. Each job's guard flags are listed as set
  (approved) or unset (dry run); lanes running right now are flagged.
  Ends with "Plan: N to run, N to retry, N to rerun, N to skip." Jobs with
  nothing left to do are summarised as "N items have completed with no
  remaining work" ("No changes." when that is everything), followed by idle
  lanes and jobs in lane scripts beyond the configured lanes.
  Fails, like swim run would, on a dependency cycle or an "# After:" that
  doesn't resolve.
`,
	"status": `swim status [N|JOB] [--yaml] [--rebuild]

  Show the last state of every lane, swim 1..N (or just one lane, by number
  or job id), from .swim/status.yml: state, job id, pending round and job
  (from lane.N.sh on disk), last round, counts,
  current or failed steps, and timing. Running lanes whose process is gone
  are shown as "running?" (interrupted).
  --yaml     print the raw status file (same as cat .swim/status.yml)
  --rebuild  reconstruct status.yml from lane scripts and logs
`,
	"step": `swim step [--label L] [--new] [--snapshot] -- cmd [args...]

  Run one command. Its output streams to the terminal unchanged; a framed copy
  goes to the log chosen by $STEP_LOG (default step.log):
    header (UTC time, shell-quoted command, cwd, git branch@sha, runtime
    version, AWS_PROFILE and header_env values), output with ANSI codes
    stripped, exit code and duration, and PASS/FAIL <label> when labelled.
  Exits with the command's exit code. On Ctrl-C the log is flushed and the
  interruption recorded before exiting.
  --label L     record PASS/FAIL L (and update status.yml when run in a lane)
  --new         start a fresh log (truncate)
  --snapshot    also save output to .swim/snapshots/laneN-<time>-<label>.txt
  Lane scripts call this through run/gate/snapshot; you rarely need it directly.
`,
	"lib": `swim lib

  Print the bash library lane scripts load. Lane scripts start with:
    _swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
  (eval, not "source <(...)": process-substitution source is broken in
  macOS bash 3.2). See "WRITING A LANE SCRIPT" in swim --help for the functions.
`,
	"archive": `swim archive N|JOB [<what>]

  Rename .swim/logs/agentN.log to agentN.prev-<what>.log in the same
  directory (<what> is slugified; it
  defaults to the archived round's job id). A job id selects the lane whose
  script holds that job or that last ran it. The next round starts a fresh
  log and history is kept. Refuses if lane N is
  running or the archive name exists. Resets the lane to idle in status.yml.
`,
	"stub": `swim stub N|JOB "<message>"

  Replace lane.N.sh with a "nothing pending" stub carrying <message>. The lane
  shows as idle and "swim run" skips it. Refuses if lane N is running.
`,
	"note": `swim note [--lane N|JOB] "<text>"

  Append a note to .swim.log, the project log, so the next agent learns it:
  a decision, a finding, why a lane was stubbed, what to do next.
  --lane N|JOB   tie the note to a lane (and its job id)
  Keep notes short and factual; never include secret values.
`,
	"log": `swim log [N|JOB] [--all]

  Print logs:
    swim log            the project log, .swim.log (one line per top-level action)
    swim log N          lane N's current log, .swim/logs/agentN.log (every round since its
                        last archive; the newest round is at the end)
    swim log N --all    all of lane N's logs: archives (.swim/logs/agentN.prev-*.log),
                        oldest first, then agentN.log, each under a header
    swim log JOB        just the rounds of that job (full id or 8+ characters),
                        from whichever current or archived log holds them
  Coloured on a terminal, plain when piped (e.g. swim log 2 | less).
`,
	"help": `swim help [command]

  Print the full guide, or one command's detail.
`,
}

// Names returns command names in a stable order.
func Names() []string {
	var n []string
	for k := range Commands {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Full is the complete --help text: the guide plus every command's detail.
func Full() string {
	s := Guide + "\nCOMMAND DETAIL\n"
	for _, order := range []string{"init", "config", "new", "plan", "run", "all", "status", "log", "step", "lib", "archive", "stub", "note"} {
		s += "\n" + Commands[order]
	}
	return s
}
