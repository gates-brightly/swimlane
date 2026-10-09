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
    - adds a marked block to .gitignore: lane.[0-9]*.sh, agent*.log, .lane*.rc, .swim/
    - creates .swim/status.yml with every lane idle
  Safe to run again.
`,
	"config": `swim config [--path]

  Print the effective configuration for this repo (defaults merged with the
  repo's section), or with --path just the config file location.
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
	"run": `swim run [N|JOB ...] [--plain]

  Run lanes. Each argument is a lane number or a job id (full, or 8+
  characters of it). A job id pins the run to that job: it runs the lane
  whose script holds it, and refuses if none does. With no arguments, runs every lane whose lane.N.sh holds a
  pending round (has a Round: line and isn't a stub). Lanes run in parallel
  as "bash lane.N.sh" from the repo root.
    - The top of the screen pins one row per lane (swim 1..N): its state and
      round. Lanes waiting on a dependency show a throbber and
      "waiting on swim N". Output scrolls below, prefixed [N].
    - Dependencies come from config. A lane starts when its dependencies in
      this run succeed and is skipped if one fails or is skipped.
    - With more than one lane, lanes get no stdin (prompts fail closed).
    - Ctrl-C reaches running lanes; lanes not yet started are skipped; the
      summary still prints.
    - Ends with a per-lane summary: result, exit code, counts, failed steps.
  Exits 0 only if every lane passed.
  --plain   no pinned panel, colour or throbber; state changes print as lines.
            Automatic when stdout isn't a terminal or NO_COLOR is set.
  Guard flags are passed through the environment:
    FIN_ALLOW_DELETE_ZG_ITEMS=1 swim run 2
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

  Rename agentN.log to agentN.prev-<what>.log (<what> is slugified; it
  defaults to the archived round's job id). A job id selects the lane whose
  script holds that job or that last ran it. The next round starts a fresh
  log and history is kept. Refuses if lane N is
  running or the archive name exists. Resets the lane to idle in status.yml.
`,
	"stub": `swim stub N|JOB "<message>"

  Replace lane.N.sh with a "nothing pending" stub carrying <message>. The lane
  shows as idle and "swim run" skips it. Refuses if lane N is running.
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
	for _, order := range []string{"init", "config", "new", "run", "status", "step", "lib", "archive", "stub"} {
		s += "\n" + Commands[order]
	}
	return s
}
