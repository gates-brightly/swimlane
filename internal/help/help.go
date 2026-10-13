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
	"config": `swim config [KEY [VALUE]] [--repo] [--path] [--lanes N]

  With no key, print the effective configuration for this repo (defaults
  merged with the repo's section), or with --path just the config file
  location. The output ends with the effective blocked_commands: the
  built-ins (git push, git commit, git pull; always on) plus the defaults'
  and the repo's additions.
  swim config KEY          print KEY's effective value
  swim config KEY VALUE    set KEY in the config file, keeping its comments
  Keys:
    chime        true | false | failure   chime when swim run/all finishes
                 (failure: only when a lane failed, was skipped or the run
                 was interrupted). Default false.
    chime_style  bell | sound | notify    bell rings the terminal bell
                 (twice on failure); sound adds a system sound, notify a
                 desktop notification, falling back to the bell. Default bell.
    chime_min_s  seconds (0 or more)      runs shorter than this never
                 chime. Default 10.
    lanes        1..99                    this repo's lane count (swim 1..N);
                 recorded in .swim.log. Refuses to drop a lane that holds a
                 pending round. Always written to the repo's section.
  Settings go under defaults: (every repo); --repo writes this repo's
  section instead. Unknown keys and bad values are rejected.
  --lanes N   same as "swim config lanes N".
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
	"run": `swim run [N|JOB ...] [--rerun] [--plain] [--no-tui] [--run-id ID] [--parallel N]
         [--chime|--no-chime] [--yaml] [--yaml-output] [--interrupt graceful|immediate] [-y|--no-prompt]

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
    - Ctrl-C (see INTERRUPTING in swim --help): the first press stops every
      lane at its next step boundary (running steps finish, no new step
      starts, lanes not yet started are skipped); the second interrupts the
      running steps (SIGINT) and kills them after interrupt_grace (5s); the
      third kills them at once. The summary still prints.
    - Interactive view: with more than one lane on a terminal, the keyboard
      picks what to read below the panel (see KEYS). The panel stays on top;
      below it is all lanes' output (the "all view") or one lane's log for
      its current round, as swim log N shows it (the "lane view"). Both
      follow new lines; scrolling up pauses ("paused · 37 new lines"). The
      all view keeps the last 20000 lines; older ones are in the logs. If
      you are in a lane view or scrolled back when the run ends, the screen
      stays until you press q ("run finished · q to close"); the summary
      prints after it closes.
    - Ends with a per-lane summary: result, exit code, counts, failed steps.
  Exits 0 only if every lane passed.
  --parallel N  run at most N lanes at once (0 = unlimited; default: config
                max_parallel, else unlimited). A lane whose dependencies have
                passed waits as "queued" for a free slot; slots go to the lane
                with the longest chain of lanes below it, then the lowest
                number. Queued lanes interrupted by Ctrl-C are skipped.
  --run-id ID   use ID as this run's id (e.g. a CI pipeline id); by default
                each run gets r-<utc>-<hex>. Lanes see it as $SWIM_RUN, and the
                lanes in the run as $SWIM_RUN_LANES.
  Shorthand: "swim 1 2" is "swim run 1 2"; "swim 3f2a9c1e" is "swim run 3f2a9c1e".
  --interrupt graceful|immediate   what the first Ctrl-C does, for this run
            (default: config interrupt, graceful). immediate interrupts
            running steps at once (the second press then kills).
  --plain   no pinned panel, colour or throbber; state changes print as lines.
            Automatic when stdout isn't a terminal or NO_COLOR is set.
  --no-tui  the pinned panel without the interactive view (output scrolls in
            the terminal as before). Also SWIM_TUI=0. The interactive view is
            also off for a single lane (it may prompt on stdin), with --plain,
            --yaml, NO_COLOR or SWIM_COLOR=0, CI=true, when stdin or stdout
            isn't a terminal, and when the terminal is too small.
  KEYS (interactive view)
    → l Tab / ← h Shift-Tab   next / previous lane, in panel order (running,
                    then waiting, then finished); from the all view → opens
                    the last lane selected, ← the last lane
    ↑ k / ↓ j       scroll one line      PgUp b / PgDn Space   a page
    Home g / End G  top; bottom and follow again
    4 2 Enter       jump to swim 42
    Enter           open the lane waiting longest for approval (see below)
    y / n           on a lane waiting for approval: approve / decline it
    f               which lanes ← → visit: every lane / running / failed
    Esc             back to the all view, following, filter cleared
    Ctrl-C          interrupt, as without the view   ?  help    q  close
                    the view after the run
  --yaml    stdout is a stream of YAML documents (schema swim.run/v1: run,
            waiting, locked, queued, start, step, finish, then summary)
            instead of the panel and table; lane output goes only to the
            logs. See READING RESULTS in swim --help. --yaml-output also
            streams lane output lines as output events.
  --chime / --no-chime   chime (or don't) when this run finishes, whatever
            config chime says. The chime comes after the summary (Ctrl-C
            too), once, if the run took at least chime_min_s seconds, stdout
            is a terminal (notify style excepted) and CI isn't true. See
            "swim config --help".
  Guard flags are passed through the environment:
    FIN_ALLOW_DELETE_ZG_ITEMS=1 swim run 2
  With a guard's flag unset and swim on a terminal, the lane asks instead:
    swim 2 requires human approval of task: delete the zg items - 2026-10-12
    (guard FIN_ALLOW_DELETE_ZG_ITEMS; or set FIN_ALLOW_DELETE_ZG_ITEMS=1 to approve without asking)  Y/N [N]
  The lane's panel row shows "awaiting approval".
    - Interactive view: several lanes can wait at once. A banner names them
      ("▶ swim 2 needs approval · Enter to view it and answer"); Enter opens
      the lane waiting longest, showing its log with the question under it,
      and there y approves, n declines. ← → still visit other lanes and Esc
      returns to all lanes; the question waits until answered.
    - Otherwise questions are asked in turn on a line below the output:
      y approves; n, Enter or Esc declines. Lane output is held meanwhile.
  An approval is logged with who answered; a decline makes the step a dry
  run. Ctrl-C declines every waiting question and stops the run as usual.
  Not asked: in CI, without a terminal, with --yaml, --no-prompt or config
  approvals: flags (then only FLAG=1 approves).
  -y, --yes     approve every guard whose flag isn't set, without asking,
                terminal or not (logged as "approved by swim run -y (<user>)");
                a flag set to anything but 1 (FLAG=0) still declines
  --no-prompt   never ask; an unset guard flag is a dry run
`,
	"all": `swim all [--rerun] [--plain] [--no-tui] [--run-id ID] [--parallel N]
         [--chime|--no-chime] [--yaml] [--yaml-output] [--interrupt graceful|immediate] [-y|--no-prompt]

  Run every lane whose pending round hasn't passed yet: the same as
  "swim run" with no lane numbers. Rounds that already passed are shown as
  "done" and not run again; failed, interrupted, skipped and new rounds run.
  --rerun   run every pending round, including ones that already passed.
  If lane scripts exist beyond the configured lanes (lane.5.sh with
  lanes: 4) holding jobs that haven't passed, swim asks whether to raise the
  lane count so they run. Without a terminal it never asks or changes
  config; it prints the "swim config --lanes N" command instead. Dependencies ("# After:" lines and config deps), the live
  view and its keys (--no-tui), guard flags, the summary and the chime (--chime / --no-chime) all
  work as in "swim run".
  Stubbed lanes and lanes without a Round: line are left alone.
`,
	"plan": `swim plan [N|JOB ...] [--rerun] [--yaml]

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
  --yaml   the plan as data (schema swim.plan/v1): each lane's action, what it
           waits on, why it would be skipped, its guards, timeout and locks.
`,
	"status": `swim status [N|JOB] [--yaml] [--rebuild] [--run RUN]

  Show the last state of every lane, swim 1..N (or just one lane, by number
  or job id), from .swim/status.yml: state, job id, pending round and job
  (from lane.N.sh on disk), last round, counts,
  current or failed steps, and timing. Running lanes whose process is gone
  are shown as "running?" (interrupted).
  --yaml     the status file's fields as YAML, tagged schema: swim.status/v1
             (with N|JOB, just that lane); the file itself is unchanged
             (cat .swim/status.yml)
  --rebuild  reconstruct status.yml from lane scripts and logs
  --run RUN  each lane as it was in swim run RUN (from the logs, archives
             included, and .swim.log for lanes skipped before they started)
`,
	"step": `swim step [--label L] [--new] [--snapshot] [--timeout D] [--retry N]
          [--backoff D] [--retry-on CODES] [--no-retry-timeout] -- cmd [args...]

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
  --timeout D   stop the command after D (default: $SWIM_STEP_TIMEOUT, from the
                script's Step-Timeout:; 0 for none); never past the round's Timeout
  --retry N, --backoff D, --retry-on CODES, --no-retry-timeout
                retry on failure (see "WRITING A LANE SCRIPT" in swim --help)
  Lane scripts call this through run/gate/snapshot; you rarely need it directly.
`,
	"lib": `swim lib

  Print the bash library lane scripts load. Lane scripts start with:
    _swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
  (eval, not "source <(...)": process-substitution source is broken in
  macOS bash 3.2). See "WRITING A LANE SCRIPT" in swim --help for the functions.
`,
	"archive": `swim archive N|JOB ... [--as NAME]

  Rename each lane's .swim/logs/agentN.log to agentN.prev-<NAME>.log in the
  same directory: swim archive 1 2 3 archives lanes 1, 2 and 3. Every
  argument is a lane number or a job id (the lane whose script holds that
  job or that last ran it). NAME is slugified; without --as each archive is
  named after its round's job id. The next round starts a fresh log and
  history is kept; each lane is reset to idle in status.yml.
  All or nothing: if any lane is running, has no log, or its archive name
  exists, nothing is archived and every problem is listed.
  The older form, a name after one lane, still works: swim archive 1 cutover.
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
	"timeline": `swim timeline [RUN|N|JOB] [--top N] [--all] [--steps] [--yaml] [--html FILE]

  Draw when each lane of a run waited, ran and finished. No argument: the
  last run; RUN: that run id (or its start, e.g. r-20261009T14). Every run
  is recorded in .swim/runs/RUN.yml when it ends, with the dependencies it
  resolved, so a later edit to a script never changes an old timeline.
      swim  2 |░░░░██████████    | 0.55 → 2.05    after 1      ◆
  Bars: █ running, ░ waiting on dependencies, ▒ queued for a slot
  (max_parallel), ▓ waiting for a lock. Failed lanes are red, skipped ones
  dim, lanes on the longest chain marked ◆. Below: the longest chain (the
  dependency path with the most running time; overhead = the run's time
  minus it), the start delay after each lane's last parent (median, p95,
  max), and the share of lane-time spent waiting.
    --top N    the N slowest lanes plus the longest chain (runs of more than
               30 lanes show 20 unless --all)
    --steps    a row per step under each lane (per stage, for rounds that use
               stage), laid out by duration from the lane's start; found in
               the current and archived logs by run id
    --yaml     the data (schema swim.timeline/v1): per-lane ready/start/end,
               waits, start delay, steps, the longest chain and its totals
    --html F   a self-contained HTML Gantt chart (inline SVG), for sharing
  N or JOB: that lane's (or job's) rounds across recorded runs, one bar each.
`,
	"log": `swim log [N|JOB] [--all] [--full] [--raw] [--yaml] [-f|--follow] [--until-done]

  Print logs:
    swim log            the project log, .swim.log (one line per top-level action)
    swim log N          lane N's current log, .swim/logs/agentN.log (every round since its
                        last archive; the newest round is at the end)
    swim log N --all    all of lane N's logs: archives (.swim/logs/agentN.prev-*.log),
                        oldest first, then agentN.log, each under a header
    swim log JOB        just the rounds of that job (full id or 8+ characters),
                        from whichever current or archived log holds them
    swim log RUN        every lane's round from that swim run, in lane order
  On a terminal the log is rendered: round and stage headers stand out,
  results are coloured, times show how far into the round they were, and
  step output longer than 40 lines is folded to its first and last 15.
  Piped (swim log 2 | less) it is the file, as cat shows it, plus the step
  running now: a step's block reaches the log file only when it ends, so
  swim log adds it from the step's spool (.swim/logs/agentN.log.partial),
  output so far included, under a result line that never appears in a file:
      RUNNING  <label> (in progress)          <elapsed>s  <hh:mm:ss>
  ("cut off: ..." instead of "in progress" if its swim step died; it is
  recorded as FAIL when the lane next runs).
  --full        don't fold long output
  --raw         print the file exactly, even on a terminal (no running step)
  --yaml        the round parsed, as data (schema swim.log/v1): the latest round,
                every round with --all, or a job's rounds; the running step is
                a result "RUNNING" with its command, output so far and
                duration_s. See READING RESULTS.
  -f, --follow  print lane N's (or JOB's lane's) current round, then follow it
                like tail -f: new log lines, and the running step's output as
                it is written (the step's block then adds only its result
                line). Runs until Ctrl-C.
  --until-done  follow until the lane has finished its round, then exit with
                the round's exit status (0 passed); on an idle lane, print its
                last round and exit with that. For scripts and services
                waiting on a job: swim log 3f2a9c1e --until-done
`,
	"ci": `swim ci [N|JOB ...] [--changed[=BASE]] [--rerun] [--junit FILE] [--provider github|gitlab|generic]
        [--require-work] [--heartbeat D] [--run-id ID]

  Run rounds in a CI job: swim all, written for a CI log viewer and built for
  commits. Detects GitHub Actions (GITHUB_ACTIONS), GitLab CI (GITLAB_CI) or
  any runner (CI=true); --provider overrides. Works outside CI too (generic).
    - Header: swim version, provider, commit and branch, the lanes selected
      and why, and the guard flags set (names only, never values).
    - Live: plain output, every lane prefixed [N], step headers and results
      included; "[N] still running: <step> (5m02s)" for a lane silent for
      --heartbeat (default 60s; 0 turns it off).
    - After the run: each lane's round log in its own section (::group:: on
      GitHub, collapsed sections on GitLab; failed lanes left open),
      annotations (FAIL/BLOCKED/STOP error, DRIFT warning, a dry-run SKIP
      notice, one notice for the lanes skipped below a failure), the
      Markdown job summary ($GITHUB_STEP_SUMMARY), --junit FILE (one suite
      per lane, one case per step), and the paths to upload as artifacts.
    - Never interactive: lanes get no stdin (confirm fails closed), no
      prompts; a lane script past the configured lanes fails with the fix,
      unless no lanes setting exists anywhere (then the highest lane script
      sets the count).
    - Pinned: the selected rounds' job ids are fixed up front; a lane
      script rewritten before its lane starts is skipped, not run.
    - Commit-tagged: SWIM_COMMIT (HEAD) goes into each round's context line
      (commit: <sha>), status.yml (commit:) and the .swim.log run, start and
      result events (commit=<sha>).
  --changed[=BASE]  only rounds whose lane.N.sh changed between BASE and HEAD,
                    plus lanes they wait on that haven't passed. The base
                    defaults from the provider: GitHub push: the event's
                    "before"; pull request: the merge base with
                    origin/$GITHUB_BASE_REF; GitLab: CI_MERGE_REQUEST_DIFF_BASE_SHA
                    or CI_COMMIT_BEFORE_SHA. An all-zero SHA (new branch) runs
                    every pending round. Needs the history (fetch-depth: 0).
  --rerun           also rounds that already passed (a test suite as rounds,
                    like this repo's own CI, always runs everything)
  --require-work    exit 1 when there is nothing to run (default: a notice, 0)
  Exit codes: 0 every selected lane passed, 1 a lane failed or was skipped,
  2 usage, config or an unresolvable # After:.
  Config on a runner comes from the repo's committed swim.yml (see CONFIG);
  commit the lane scripts the job runs (git add -f lane.N.sh, or drop
  lane.[0-9]*.sh from swim's .gitignore block). swim never commits or pushes
  results: upload .swim/logs/, .swim/snapshots/, .swim.log and the JUnit file
  as artifacts. "Already passed" lives in .swim/status.yml: a fresh runner
  reruns every pending round unless --changed narrows it (or .swim/ is
  restored from a CI cache).
`,
	"queue": `swim queue [list] [--all] [--yaml]
swim queue add [N|JOB ...] [--approve FLAG[,FLAG]]... [-y] [--parallel N] [--note TEXT] [--wait]
swim queue N|JOB ... [flags]     (shorthand for swim queue add)
swim queue show|rm|requeue ID

  Queue a batch of rounds to run as soon as swim is idle, by a worker
  (swim watch). Get a batch ready, queue it with its approvals, and let it
  fire when the current work is done.
  add       queue the named lanes, or with none every pending round that
            hasn't passed (as swim all would run). The batch is fixed now:
            each lane is pinned to the job it holds, so a lane rewritten
            before the batch runs is skipped, not run.
            --approve FLAG  approve that guard for this batch (repeat, or
                            FLAG,FLAG); it must be a guard of a lane in the
                            batch, so a typo fails here, not at 3am
            -y, --yes       approve every guard in the batch
            --wait          then block like swim wait, and exit with the
                            batch's result (0 passed, 1 otherwise)
            A queued batch never asks: guards it doesn't approve are dry
            runs, and a guard flag in the worker's own environment does not
            count (the queue entry alone says what may change).
  list      waiting, running and orphaned entries, and the last 5 consumed
            (--all: every one); --yaml for scripts (schema swim.queue-list/v1)
  show ID   the entry file; ID is its id, a 4+ character prefix, or #seq
  rm ID     remove an entry that isn't running
  requeue ID  put an orphaned (its worker died) or consumed entry back at the
            end of the queue
  Entries are files under .swim/queue/: NNNN-q-<utc>-<hex>.queue while
  waiting, .running once a worker has claimed it (by renaming it, which
  is atomic: two workers can never claim the same entry), then
  done/NNNN-....yml with its result (run id, exit, each lane's result).
  .swim.log records queue, queue-run and queue-done events.
`,
	"watch": `swim watch [--once] [-y] [--approved] [--interval D]

  The queue's worker. Runs each queued batch (swim queue add), oldest
  first, as soon as swim is idle in this repo: no lane running and no other
  swim run waiting on lanes. It runs the batch like swim run with the
  batch's lanes pinned and its approvals, never prompts, and consumes the
  entry when the run ends (passed, failed or interrupted alike: a half-run
  change is never retried on its own; swim queue requeue does that).
  One batch runs at a time per repo (a worker lock), so several workers
  can share a queue.
  --once         run every queued batch, then exit (non-zero if one failed)
  --approved     also queue, by itself, every lane whose exact script is fully
                 approved (swim approve, meeting lookup.yml's rules), once per
                 approval: the last approval is what makes it run
  -y, --yes      approve every guard in every batch, beyond what the batch
                 approves (the worker's say-so: logged as "approved by swim
                 watch -y (<user>)", and in the consumed entry as approved_by).
                 The batch's own --approve and -y keep their credit.
  --interval D   how often to check the queue and for idleness (default 2s)
  Ctrl-C: during a batch, the usual graceful stop (lanes stop at a step
  boundary; again to force quit), then the batch is consumed as interrupted
  and the watch exits; between batches, the watch exits at once. Batches
  still waiting stay queued.
`,
	"wait": `swim wait [--interval D] [--timeout D]

  Block until swim is idle and nothing new is about to start: no lane
  running, no swim run waiting on lanes, and no batch queued or running
  (an orphaned batch never runs, so it doesn't count). Prints what it is
  waiting on whenever that changes. Use it in scripts to wait out the
  current work and the queue behind it; swim queue add --wait does the
  same after queueing. With batches queued and no worker up, it waits
  until one starts (swim watch).
  --interval D   how often to check (default 2s)
  --timeout D    give up after D (exit 1, saying what is still busy)
`,
	"approve": `swim approve                                    list approvals and standing approvals
swim approve N|JOB [--guards A,B | --all] [--expires D] [--note T] [--key K]
swim approve --auto USER [--guards A,B | --all] [--key K]

  How approvers act. Every approval is an SSH-signed event in a committed
  file under approvals/, so an approver anywhere (with the repo) can add
  theirs and push it; the files are hash-linked logs of approve and revoke
  events, so their order is attested like commits.
  swim approve N   approve this exact lane script (its SHA-256): its guards
                   (--guards, or --all) approve without a prompt or flag when
                   the approvers, named by lookup.yml, meet lookup.yml's rule
                   for each guard. Another approver runs the same command to
                   co-approve (the statement can't change). Writes
                   approvals/lane.N.<sha12>.signature. If the script changes
                   it no longer matches: approve the new version.
  --auto USER      a standing approval: every job USER queues (swim queue add,
                   signed with USER's key) is approved, by you, for the guards
                   given (default every guard). It is never consumed: it
                   stands until you revoke it. Writes approvals/auto.USER.signature.
  Approvals of a script and standing approvals for its queuer count
  together toward a rule (zach standing for bob + alice approving the script
  = 2 of platform-admin).
  --key K    sign with this key (default: config signing_key, then
             ~/.ssh/id_ed25519; a .pub path signs through ssh-agent)
  swim watch --approved runs fully approved scripts by itself.
`,
	"revoke": `swim revoke N|JOB [--key K]
swim revoke --auto USER [--key K]

  Withdraw your approval: of lane N's script (every version you approved),
  or your standing approval of USER's queued jobs. Adds a signed revoke
  event; your approval stops counting from then on (approving again adds
  another approve event). A round that already ran keeps its record.
`,
	"verify": `swim verify [--strict]

  Prove the chain of custody (.swim/chain.jsonl) is intact:
    - every entry follows the one before it (sequence and previous-hash):
      a changed, removed or reordered entry breaks every link after it
    - every signature is valid for its signer's key
    - every round's log section still hashes to what was recorded (a log
      edited after the round ended is caught), and the copy of its script
      kept under .swim/scripts/<sha256>.sh is unchanged
    - every signed approval's signatures check out
  and checks every approvals file under approvals/ event by event (listing
  each, and whether a script approval is stale: the script changed since). Exits 1 on any break. --strict also fails on unsigned
  entries and on signers not in lookup.yml.
`,
	"history": `swim history [RUN|N|JOB] [--yaml]

  The chain of custody, oldest first, around who approved and who ran
  what: each approval (guard, action, approvers by name and group from
  lookup.yml, and how: signature, prompt, flag, auto, queue) and each round
  (result, exit, script and log hashes, approvals used), with the entry's
  hash, run and signer. --yaml: the entries as data (schema swim.history/v1).
  swim verify checks it.
`,
	"keys": `swim keys

  Who's who: lookup.yml's users with their key fingerprints, groups (with
  nested groups expanded), require rules, and the key this machine signs
  with and whom it maps to.
  lookup.yml (repo root, committed):
      users:
        zach:
          keys:
            - ssh-ed25519 AAAA... zach@laptop
            - sk-ssh-ed25519@openssh.com AAAA... zach@yubikey
        alice:
          keys: [ssh-ed25519 AAAA... alice@work]
      groups:
        platform-admin: [zach, alice]
        ops: [bob, platform-admin]       # a group may include groups
      require:                           # what a signed approval needs
        - guards: [FIN_ALLOW_*]          # globs; the first matching rule applies
          group: platform-admin
          count: 2                       # distinct users
        - group: ops                     # no guards: every other guard
  With no rule a signature from any listed user approves; with no
  lookup.yml signatures don't count at all (their keys can't be named).
`,
	"interrupt": `swim interrupt N|JOB

  Ask one running lane to stop at its next step boundary, from any
  terminal: its current step finishes, no further step starts, and the round
  ends interrupted (STOP  interrupted by operator (after: <step>), exit 130).
  Other lanes keep running. The same thing the first Ctrl-C does to every
  lane of a swim run.
`,
	"lint": `swim lint [N|JOB ...] [--strict] [--yaml]

  Check lane scripts before they run; changes nothing. With no lanes, lints
  every lane script (stubs skipped). Each finding is printed as
      lane.2.sh:5  error  <what is wrong>  [code]
                          fix: <how to fix it>
  then a summary line ("2 errors, 1 warning"). Exit 1 on any error; with
  --strict (for CI), on warnings too. --yaml prints a swim.lint/v1 document.

  Checks [code] (level):
    round (error)          no # Round: line
    placeholder (error)    a template placeholder left in
    after (error)          # After: names an unknown lane or job, or itself
    cycle (error)          lanes wait on each other in a cycle
    set-e (error)          set -e / set -o errexit (failed checks must go on)
    lane-init (error)      no lane_init, or lane_init with another lane's number
    summary (warn)         summary missing or not the last line
    stage (error)          an unknown stage name (the round would stop there)
    stage-order (warn)     stages out of order, repeated, or change first
    bash4 (warn)           declare -A, mapfile, ${x,,}, |&, coproc, ... (macOS
                           has bash 3.2)
    guard-unlisted (warn)  a guard flag used but not listed in # Guards:
    guard-unused (warn)    a # Guards: flag no guard uses
    destructive (warn)     delete/destroy/drop/rm -r outside an if guard block
    blocked (error)        git push/commit/pull or a blocked_commands pattern
    secret-echo (warn)     echo/printf of a secret variable
    header (warn)          a Timeout, Step-Timeout or Locks value swim ignores
    cross-lane (info)      uses a path another lane writes (> path, -o path,
                           cp/mv/tee) without either waiting on the other
    syntax                 an older (warn) or newer (error) script syntax
    lint-ignore            a malformed disable comment

  Disable comments need a check code and a reason:
    cmd   # swim:lint-ignore CODE[,CODE] reason    this line
    # swim:lint-ignore CODE reason                 the next line
    # swim:lint-ignore-file CODE reason            the whole script
  Without a reason the comment is an error and disables nothing.

  swim run lints the lanes it is about to run and refuses to start (exit 2)
  on set-e and lane-init errors; blocked commands, After: and cycles it
  refuses on its own.
`,
	"doctor": `swim doctor [--fix] [--strict] [--yaml]

  Check swim's environment and this repo; prints findings like swim lint
  and exits 1 on any error (--strict: on warnings too; --yaml: a
  swim.doctor/v1 document). Checks [code] (level):
    binary-platform (error)   this swim, bin/swim or a swim on PATH is built
                              for another OS/CPU (read from its ELF/Mach-O
                              header; nothing is run)
    path-missing (warn)       no swim on PATH
    path-multiple (warn)      more than one swim on PATH: which runs, which hide
    swim-bin (warn)           $SWIM_BIN, or the first swim on PATH, isn't this one
    config-parse (error)      the config file doesn't load
    config-repo (warn)        no config section for this repo (swim init)
    lanes-beyond (warn)       lane scripts above the configured lane count
    gitignore (warn)          swim's .gitignore block missing or out of date
    tracked (warn)            git tracks lane scripts, .lane*.rc or .swim/
    stale-pid (warn)          .swim/laneN.pid naming a process that is gone
    toolchain (error)         config toolchain fails in bash -c (30s limit)
    git (error)               git missing, or not inside a git repo
    header-env-secret (warn)  header_env lists a secret variable
    lock (info/warn)          .swim.lock missing (info), invalid or for
                              another breaking version (warn)

  --fix applies only safe local repairs, then checks again: it rewrites
  swim's .gitignore block and removes stale pid files. It never edits config,
  lane scripts or git.
`,
	"changelog": `swim changelog [-n N] [--since VERSION] [--all]

  What changed in swim, from the changelog built into this binary (the
  repo's CHANGELOG.md when it was built). Run it after updating to see what
  is new. Works anywhere; it needs no repo.

  swim changelog                  the newest revision (default -n 1)
  swim changelog -n 3             the newest three revisions
  swim changelog --since 2.20261009
                                  every revision after that version (or tag,
                                  v0.2.20261009, or a pre-release commit id)
  swim changelog --all            every revision

  A revision is one "## " section: Unreleased (on a build from main), a
  release version <breaking>.<YYYYMMDD>, or a commit for revisions before
  versioning. A revision with a new breaking number has Upgrading notes:
  read them, and swim lock --help, before running lanes.
`,
	"lock": `swim lock [--upgrade]

  swim's version is <breaking>.<YYYYMMDD> (swim --version), e.g. 1.20261009:
  the breaking version changes when lane scripts, logs or swim's state
  change incompatibly, or for a second release on the same day; the date is
  the build date.

  .swim.lock, at the repo root, pins the breaking version a repo's lanes use.
  swim writes it on the first run (swim run / swim all, or a lane script run
  directly) if there is none. Commit it, so everyone runs a compatible swim.

  When the lock and this swim disagree, swim refuses to run lanes or write
  new ones (swim plan / status / log still work, with a warning):
    - lock is newer than this swim: update swim, then run again:
        go install github.com/gates-brightly/swimlane/cmd/swim@latest
        (or in a swimlane checkout: git pull && make install)
    - lock is older than this swim: the repo was set up before a breaking
      change. Read the changes below, update the lane scripts (swim new
      writes the current template), let running lanes finish, then record
      that the repo is ready with: swim lock --upgrade

  swim lock             show this swim's version and the repo's lock
  swim lock --upgrade   move the lock up to this swim's breaking version

  Breaking versions:
    7   v0.7.20261013: a deliberate bump (swim watch -y is new; nothing
        incompatible). Lane scripts, logs and state are as in 4-6:
        swim lock --upgrade
    6   v0.6.20261012: the third release that day (swim queue and swim watch
        are new; nothing incompatible). Lane scripts, logs and state are as
        in 4 and 5: swim lock --upgrade
    5   v0.5.20261012: the second release that day, so a new breaking number
        with nothing incompatible. Lane scripts, logs and state are as in 4:
        swim lock --upgrade
    4   v0.4.<YYYYMMDD>. Syntax 2 lane scripts and logs (migrated
        automatically), BLOCKED results (exit 87), new lane states
        (starting, queued, locked), graceful Ctrl-C by default (exit 137
        counts as interrupted), swim.yml. Read swim changelog --since 2.20261009,
        then: swim lock --upgrade
    3   v0.3.20261009 only, retracted: tagged without the bump, so it reports
        itself as 2.20261009. No lock was ever written at 3.
    2   first published release (tag v0.2.<YYYYMMDD>). .swim/status.yml now
        records the round a skipped lane skipped (job, round, finished_at),
        which swim plan uses to show it as a retry. Lane scripts are unchanged:
        a repo locked at 1 only needs: swim lock --upgrade
    1   pre-release: lane.N.sh scripts with Round:/Job:/After: headers,
        .swim/ state and logs, .swim.log project log
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
	for _, order := range []string{"init", "config", "new", "plan", "run", "all", "status", "log", "timeline", "step", "lib", "archive", "stub", "note", "lock", "changelog", "interrupt", "lint", "doctor"} {
		s += "\n" + Commands[order]
	}
	return s
}
