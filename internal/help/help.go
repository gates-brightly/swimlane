// Package help holds swim's user and agent documentation: the short
// overview `swim --help` prints on a terminal, the full guide (swim guide,
// and swim --help anywhere else, so agents still get it) and per-command
// detail.
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
    - adds a marked block to .gitignore: lane.[0-9]*, .env.swim, .swim/,
      .swim.log
    - creates .swim/status.yml with every lane idle
    - records "init" in .swim.log, the project log (git-ignored too; drop it
      from the block if you want to commit the project's history)
  Safe to run again.
`,
	"config": `swim config [KEY [VALUE]] [--repo] [--path]

  With no key, print the effective configuration for this repo (defaults
  merged with the repo's section), or with --path just the config file
  location. The output ends with the effective blocked_commands: the
  built-ins (git push, git commit, git pull; always on) plus the defaults'
  and the repo's additions.
  swim config KEY          print KEY's effective value
  swim config KEY VALUE    set KEY in the config file, keeping its comments
  Keys:
    outputs_keep duration (14d, 36h)      round outputs older than this that no
                 lane's latest round uses are pruned by swim doctor --fix.
    msg_log_interval  duration            at most one MSG log line per lane per
                 this (default 10s; 0 logs every change of swim msg).
    queue_lease  duration (10s or more)   a queue swimmer renews its claim's
                 heartbeat every third of this while it runs a batch; seen
                 from another host, a claim with an older heartbeat is
                 orphaned. Default 2m.
    claim_lease  duration (1m or more)    how long a lane claim (swim new,
                 swim claim) lasts unless refreshed. Default 2h.
    watch_batches  a number (1 or more)   how many queued batches one swim
                 watch runs at once (default 4; swim watch --batches N).
    cache_dir    an absolute path         where tool caches live (# Cache:;
                 default ~/.cache/swim, macOS ~/Library/Caches/swim; under
                 /tmp it gets your user id). Not in a repo's swim.yml.
    cache_max    a size (10GB, 500MB, 0)  a run trims the repo's caches to
                 this, least recently used first (default 10GB; 0: no cap).
    cache_slug   a name                   the repo's cache directory (default
                 its name and a hash of its origin remote). Not in swim.yml.
    runners.EXT  a command                the runner for lane.N.EXT scripts with
                 no #! line, e.g. swim config runners.ts tsx. Always written
                 to the repo's section; swim.yml's runners: is the default.
  Settings go under defaults: (every repo); --repo writes this repo's
  section instead. Unknown keys and bad values are rejected.
  There is no lane count to set: swim shows the lanes in use (a lane script
  that isn't a stub, a current log, or a running lane). swim new N starts
  one (1..99); swim archive N drops one. A lanes: setting is ignored.
  Config lives outside the repo, in ~/.config/swim/config.yml
  ($XDG_CONFIG_HOME/swim/config.yml if set). Swimmers must not edit it; give
  the operator exact YAML lines instead.
`,
	"new": `swim new N|TASK "<round goal>" [--job ID] [--force]

  swim new TASK (a name: renew-certs) writes the lowest lane not in use
  and names it with a # Task: line, so swim run TASK, approve TASK, log
  TASK, status and queue find it by name. The lane number stays
  what signatures and the chain record; the name is an alias.
  A name is a-z, 0-9 and - (a letter first, at most 40, not only hex
  digits and -), not a swim command, and one lane's alone (swim lint).
  Where swim takes a lane it reads a number as a lane, then a name as a
  task, then a job id; swim run "<label>" -- cmd (in a lane) is a step.
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
         [--yaml] [--yaml-output] [--interrupt graceful|immediate] [-y|--no-prompt]
swim run [options] "<label>" -- cmd [args...]     (inside a lane: one step; see OTHER LANGUAGES)

  Run lanes. Each argument is a lane number or a job id (full, or 8+
  characters of it). A job id pins the run to that job: it runs the lane
  whose script holds it, and refuses if none does. Named lanes always run,
  even if their round already passed (rounds are rerunnable).
  With no arguments (or "swim all"), runs every lane whose lane.N.sh holds a
  pending round (a Round: line, not a stub) that hasn't passed yet; rounds
  that passed show as "done". --rerun runs them too.
  Lanes run in parallel as "bash lane.N.sh", each in its staged copy.
    - The top of the screen pins one row per lane in use: its state and
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
    - Ctrl-C (see INTERRUPTING in swim guide): the first press stops every
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
            logs. See READING RESULTS in swim guide. --yaml-output also
            streams lane output lines as output events.
  Guard flags are passed through the environment:
    FIN_ALLOW_DELETE_ZG_ITEMS=1 swim run 2
  With a guard's flag unset and swim on a terminal, the lane asks instead:
    swim 2 requires human approval of guard FIN_ALLOW_DELETE_ZG_ITEMS
      job 3f2a9c1e-... · manifest 3be9a1c07d2e
      the lane says: delete the zg items - 2026-10-12
    (or set FIN_ALLOW_DELETE_ZG_ITEMS=1 to approve without asking)  Y/N [N]
  The first lines are swim's own: the lane, job and manifest the launcher
  started (each lane process has its own approval token), not what the
  request claims. A flag neither the script nor a file it includes declares
  or uses is refused (unless the script names a guard's flag at run time,
  guard "$F"), and an approval is used once, by the lane it was given to.
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
                a flag set to anything but 1 (FLAG=0) still declines. What it
                approved stays loud: swim status (auto_approved), the round's
                chain entry and swim history, and swim ci's summary
  --no-prompt   never ask; an unset guard flag is a dry run
  Each lane runs a staged copy of its files (.swim/runs/<run>/<N>/), fixed
  before it waits for anything and checked again right before it starts;
  an edit to the working tree after that changes nothing in the run. See
  EXECUTION MANIFEST in swim guide.
`,
	"all": `swim all [--rerun] [--plain] [--no-tui] [--run-id ID] [--parallel N]
         [--yaml] [--yaml-output] [--interrupt graceful|immediate] [-y|--no-prompt]

  Run every lane whose pending round hasn't passed yet: the same as
  "swim run" with no lane numbers. Rounds that already passed are shown as
  "done" and not run again; failed, interrupted, skipped and new rounds run.
  --rerun   run every pending round, including ones that already passed.
  Every lane script counts, whatever its number. Dependencies ("# After:" lines and config deps), the live
  view and its keys (--no-tui), guard flags and the summary all work as in "swim run".
  Stubbed lanes and lanes without a Round: line are left alone.
`,
	"report": `swim report [N|JOB ...] [--earmark] [--name NAME] [--since T|RUN|JOB] [--no-logs] [--results] [--yaml]
swim report --earmark --wait [--timeout D] [--interval D]
swim report --earmarks | --earmark --reset [--name NAME]

  What changed since you last looked, in one call, untruncated: the lanes
  whose state or job changed (as swim status words them, with what they
  were, then one unchanged: line), held locks and the queue, every
  .swim.log line written since, and every lane-log line written since:
  the rest of the round that was in progress, then each round after it
  verbatim, as swim log --raw prints
  them. Steps running now follow, from their spools. It changes nothing.
  --earmark      then move your earmark to the end of what it printed: the
                 next report starts there. Lines written while it printed
                 are in the next one; a running step stays new until it
                 ends. An earmark is a place in the record, never an edit:
                 nothing is cut from a log.
  --name NAME    whose earmark (default $SWIM_SESSION, else "default"):
                 readers each keep their own, in .swim/earmarks/NAME.yml
  --since T|RUN|JOB  start at a UTC time, a run's first .swim.log line or a
                 job's first round, whatever the earmark says
  N|JOB ...      only these lanes (their .swim.log lines, logs and status);
                 the earmark still keeps the other lanes' places, so the
                 next full report misses nothing
  --no-logs      status and .swim.log only
  --results      lane logs' result lines only: each step's command and
                 output are dropped, counted, with where to read them
  --yaml         swim.report/v1: earmark, status {changed, unchanged, queue},
                 history (lines), lanes: [{lane, rounds:
                 [{continues_from, round: swim.log/v1, its results since}]}]
  --wait         block until there's something new, then report (with
                 --earmark: the agent loop in one command); --timeout D
                 gives up, failing as swim wait does
  --earmarks     list every reader's earmark (name, time, age)
  --reset        (with --earmark) delete your earmark: the next report is
                 the work in progress again
  With no earmark yet the report is the work in progress: every lane's
  current (not archived) rounds, and .swim.log from the oldest of them.
  An earmark that lost its place (a round pruned, a log rewritten) says so
  with a WARN line and shows the rounds since it was set.
`,
	"status": `swim status [N|JOB] [--yaml] [--rebuild] [--run RUN]

  Show the last state of every lane in use (or just one lane, by number
  or job id), from .swim/status.yml: state, job id, pending round and job
  (from lane.N.sh on disk), last round, counts,
  current or failed steps, and timing. Running lanes whose process is gone
  are shown as "running?" (interrupted); the next swim run, swim all, swim
  ci or swim watch ends their round as interrupted. Guards the round's run
  approved with no one asked are listed as "approved without a prompt": by
  -y (auto_approved, auto_via: swim run -y, queue q-... -y or swim watch
  -y) or by a guard flag (flag_approved: FLAG=1, a batch's --approve).
  Guards it didn't approve, whose steps ran as dry runs, show as [DRY]
  (dry_run), here, in the live view and the watch screen.
  Under each lane holding a pending round (not one live in a run, nor one
  that already passed), what its header declares and where that stands:
      after swim 1 (pending: a dry run or failure there skips this)
      guard DROP_OK  drop table old_orders
             not approved: approval needs 2 signature(s) from dba, has 1; ...
      timeout 30m · locks orders-db · includes lib/tables.sh
  Each lane it waits on is passed, pending, running, failed, skipped,
  interrupted or "no round"; each guard is decided as swim run would as
  it starts now (FLAG=1 in this environment, signed approvals against
  lookup.yml, a rule nothing can meet), and says whether swim run would
  ask or dry-run. Only what the header sets is shown (Fail: hard, a
  target, an owner too).
  --yaml     the status file's fields as YAML, tagged schema: swim.status/v1;
             each pending lane adds pending_header {after, guards: [{flag,
             description, approved, how, why, asks, never}], timeout,
             locks, includes, fail, targets, owner} and deps: [{lane, job,
             state, note}] (deps_error if its After: doesn't resolve);
             claims: the lanes' live claims (swim claim), by lane
             (with N|JOB, just that lane); the file itself is unchanged
             (cat .swim/status.yml)
  --rebuild  reconstruct status.yml from lane scripts and logs
  --run RUN  each lane as it was in swim run RUN (from the logs, archives
             included, and .swim.log for lanes skipped before they started)
`,
	"step": `swim step [--label L] [--new] [--snapshot] [--timeout D] [--retry N]
          [--backoff D] [--retry-on CODES] [--no-retry-timeout]
          [--expect-fail[=CODES]] -- cmd [args...]

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
                script's Step-Timeout:; 0 for none); never past the round's Timeout.
                The command runs in its own process group: SIGTERM to all of
                it, SIGKILL after 5s (when stdin is a terminal, only to the
                command: see "TIME LIMITS" in swim guide)
  --retry N, --backoff D, --retry-on CODES, --no-retry-timeout
                retry on failure (see "WRITING A LANE SCRIPT" in swim guide)
  --expect-fail[=CODES]
                the command should fail: the step passes on a non-zero exit
                (one of CODES, e.g. =1,2, if given) and is logged "failed as
                expected: exit N"; exit 0 or another code fails it. A time
                limit or a signal still fails it. Not with --retry.
  Lane scripts call this through run/gate/snapshot; you rarely need it directly.
`,
	"gate": `swim gate [options] "<label>" -- cmd [args...]

  In a lane script in any language (see OTHER LANGUAGES in swim guide): run
  one step like swim run "<label>" -- cmd; if it fails, record STOP and end
  the round, stopping the program even if it carries on. Options as for
  swim run's in-lane form: --timeout D, --retry N, --backoff D,
  --retry-on CODES, --no-retry-timeout, --expect-fail[=CODES]. (In a bash
  lane, use the library's
  gate.)
`,
	"snapshot": `swim snapshot [options] "<label>" -- cmd [args...]

  In a lane script in any language: run a read-only step and save its output
  to .swim/snapshots/laneN-<time>-<label>.txt.
`,
	"guard": `swim guard FLAG "<action - date, reason>"

  In a lane script in any language: exit 0 if the operator approves the
  action, 1 if not. FLAG=1 approves up front; unset, swim asks the operator
  (or counts signed approvals); anything else records SKIP as a dry run.
  Under swim run the launcher decides: FLAG=1 counts only if swim run's own
  environment set it (a FLAG=1 the lane sets is a dry run), and signed
  approvals count as they were when the run started.
  One flag per destructive action; list it in the header (or lane.N.yml):
  under swim run, a flag the script neither lists nor uses by name is
  refused. With # Observe: probes, a fact that changed between approval and
  now ends the round as drift (exit 3).
`,
	"stage": `swim stage snapshot|check|change|verify

  In a lane script in any language: start a stage. Out of order or twice
  records WARN; an unknown name ends the round.
`,
	"drift": `swim drift "<what differs>"

  In a lane script in any language: record DRIFT (deployed state differs
  from code).
`,
	"stop": `swim stop "<reason>"

  In a lane script in any language: record STOP <reason> and end the round
  now (exit 1); the program is stopped. It runs only inside a lane.
`,
	"env": `swim env [N|JOB] [--yaml]

  The environment env files give lane N (with no lane: the general and repo
  keys), masked as the lane's logs show it, with where each key comes from
  (file and line) and whether the shell overrides it. Exits 1 if a key is
  one of the lane's guard flags. --yaml: a swim.env/v1 document with the
  env_sha256 the round would record. See ENV FILES in swim guide.
`,
	"msg": `swim msg [--progress N/M] "<text>"

  In a lane (bash, any language, or a command inside a step): post the
  lane's status message, where a long job is now. The live view, swim
  status and swim run --yaml (msg events) show it; the latest
  wins; msg "" clears it, and so does the round's end. Text is one line,
  cleaned (no ANSI or control characters), masked, at most 120 characters.
  The log gets MSG lines for changes, at most one per msg_log_interval
  (default 10s; 0 logs every change), and the latest when a step or the
  round ends. Never fails the round: outside a lane it warns and exits 0.
`,
	"input": `swim input N|JOB NAME

  In a lane: print the path of an output (# Outputs:) of a round this one
  depends on: lane N's round this run waited on (it must have passed), or
  job JOB's latest passed round. Only lanes and jobs in the round's After:
  (or config deps). A small value (swim output) prints the value. What was
  read, and its hash, goes in the chain, and swim verify proves it matches
  what the producer made. A failure records FAIL and exits 1.
`,
	"output": `swim output KEY VALUE

  In a lane: record a small value (an id, a version) among the round's
  outputs, for dependents to read with swim input N KEY.
`,
	"lib": `swim lib

  Print the bash library lane scripts load. Lane scripts start with:
    _swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
  (eval, not "source <(...)": process-substitution source is broken in
  macOS bash 3.2). See "WRITING A LANE SCRIPT" in swim guide for the functions.
`,
	"clean": `swim clean [--queue|--no-queue] [--all] [--no-input] [-n] [-o yaml]

  Idle every lane whose round is finished (passed, failed, interrupted,
  stale), in one go: its logs are archived under one name,
  clean-YYYYMMDD-HHMM, as swim archive does (agentN.prev-clean-....log;
  status shows it idle), and its script is replaced by a stub, as swim
  stub does. A script edited since its round ran (new work), a stub, or a
  lane another session has claimed keeps its script: only its logs are
  archived. Running and waiting lanes are left alone. Then it asks,
  defaulting to no, whether to remove the waiting queue batches (a batch
  someone else queued and signed is asked about separately; orphans are
  only listed). Without a terminal (CI, an agent, --no-input) it archives
  and stubs, and keeps the queue. --queue/--no-queue answer up front;
  --all is --queue. Not -y: that approves guards.
  -n, --dry-run   print the plan; change nothing
  -o yaml         the plan, or what it did, as swim.clean/v1
  Refused while swim is busy (a run or swimmer holds lanes). Recorded in
  .swim.log (clean, and archive/stub/queue-rm per item) and the chain (a
  clean entry: the stubbed scripts' SHA-256s, the removed batches); swim
  verify still finds the archived logs, and the scripts that ran are kept
  by hash in .swim/scripts/.
`,
	"archive": `swim archive N|JOB ... [--as NAME] [--force]

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
	"stub": `swim stub N|JOB "<message>" [--force]

  Replace lane.N.sh with a "nothing pending" stub carrying <message>. The lane
  shows as idle and "swim run" skips it. Refuses if lane N is running.
`,
	"note": `swim note [--lane N|JOB] "<text>"

  Append a note to .swim.log, the project log, so the next agent learns it:
  a decision, a finding, why a lane was stubbed, what to do next.
  --lane N|JOB   tie the note to a lane (and its job id)
  Keep notes short and factual; never include secret values.
`,
	"log": `swim log [N|JOB] [--all] [--full] [--raw] [--yaml] [-f|--follow] [--until-done]
         [--failures]

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
      prompts.
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
  --rerun           also rounds that already passed
  --require-work    exit 1 when there is nothing to run (default: a notice, 0)
  Exit codes: 0 every selected lane passed, 1 a lane failed or was skipped,
  2 usage, config or an unresolvable # After:.
  Config on a runner comes from the repo's committed swim.yml (see CONFIG);
  commit the lane scripts the job runs (git add -f lane.N.sh, or drop
  lane.[0-9]* from swim's .gitignore block). swim never commits or pushes
  results: upload .swim/logs/, .swim/snapshots/, .swim.log and the JUnit file
  as artifacts. "Already passed" lives in .swim/status.yml: a fresh runner
  reruns every pending round unless --changed narrows it (or .swim/ is
  restored from a CI cache).
`,
	"queue": `swim queue [list] [--all] [--yaml]
swim queue add [N|JOB ...] [--approve FLAG[,FLAG]]... [-y] [--parallel N] [--note TEXT] [--wait] [--timeout D]
swim queue N|JOB ... [flags]     (shorthand for swim queue add)
swim queue show|rm|requeue ID

  Queue a batch of rounds to run as soon as its lanes are free, by a
  swimmer (swim watch). Get a batch ready, queue it with its approvals, and let it
  fire when the current work is done.
  add       queue the named lanes, or with none every pending round that
            hasn't passed (as swim all would run). The batch is fixed now:
            each lane is pinned to the job it holds, its script, its
            manifest (what it sources, its sidecar, swim.yml's policy) and
            its env, so a lane changed before the batch runs is skipped, not
            run.
            --approve FLAG  approve that guard for this batch (repeat, or
                            FLAG,FLAG); it must be a guard of a lane in the
                            batch, so a typo fails here, not at 3am
            -y, --yes       approve every guard in the batch
            --wait          then block like swim wait, and exit with the
                            batch's result (0 passed, 1 otherwise)
            --timeout D     give up waiting after D (implies --wait): exit 1,
                            saying what is still busy; the batch stays queued
            A queued batch never asks: guards it doesn't approve are dry
            runs, and a guard flag in the swimmer's own environment does not
            count (the queue entry alone says what may change).
            A batch that approves none of its lanes' guards (and no
            standing approval of yours covers) warns on stderr, naming
            each lane and the guards that will dry-run.
            Swimmers say which swim they are (.swim/workers/): with swimmers
            up and none able to run the batch (its swim reads other
            syntaxes than .swim.lock pins, or older than a lane's script),
            add refuses, naming each swimmer's swim and this one; with no
            swimmer up it queues and warns. The entry records the swim that
            queued it (swim, syntax).
  list      waiting, running and orphaned entries, and the last 5 consumed
            (--all: every one); --yaml for scripts (schema swim.queue-list/v1).
            A batch a swimmer leaves queued says why ("left queued: ...")
  show ID   the entry file; ID is its id, a 4+ character prefix, or #seq
  rm ID     remove an entry that isn't running
  requeue ID  put an orphaned (its swimmer died) or consumed entry back at the
            end of the queue
  A claimed entry is orphaned when its swimmer is gone: on the swimmer's host,
  its process (pid and start time) is; from another host sharing the queue,
  its heartbeat is older than its lease (queue_lease, default 2m). A claim by
  an older swim (no heartbeat) can't be judged from another host and shows
  as running?.
  Entries are files under .swim/queue/: NNNN-q-<utc>-<hex>.queue while
  waiting, .running once a swimmer has claimed it (by renaming it, which
  is atomic: two swimmers can never claim the same entry), then
  done/NNNN-....yml with its result (run id, exit, each lane's result).
  .swim.log records queue, queue-run and queue-done events.
`,
	"watch": `swim watch [--once] [-y] [--approved] [--interval D] [--batches N] [--no-tui]

  The queue's swimmer. Runs queued batches (swim queue add) side by side,
  up to --batches at once (watch_batches, default 4), each in a process
  of its own: a batch starts as soon as none of its lanes is in another
  batch or another swim run, oldest first, so a later batch whose lanes
  are free starts ahead of one that has to wait. Lanes that depend on
  (After:) or share locks with a lane of another batch wait for it as
  they do across swim runs. It runs each batch like swim run with the
  batch's lanes pinned and its approvals, never prompts, and consumes the
  entry when the run ends (passed, failed or interrupted alike: a half-run
  change is never retried on its own; swim queue requeue does that).
  Several swimmers can share a queue: claims are atomic, and a swimmer
  picks batches under a lock, so two never take the same lanes. While a
  batch runs, its process renews the claim's heartbeat every third of
  queue_lease (swim config). A batch this swimmer's swim can't run (the
  lock was upgraded after it started, or a lane's script is newer than it
  reads) is left queued for a swimmer that can, with why, in swim queue and
  .swim/workers/<host>-<pid>.yml, which says which swim the swimmer is. As
  it starts, the
  watch ends the round of any lane whose process died without ending it
  (as swim run does): END INTERRUPTED in its log, interrupted in status.
  --once         run every queued batch, then exit (non-zero if one failed)
  --approved     also queue, by itself, every lane whose exact script is fully
                 approved (swim approve, meeting lookup.yml's rules), once per
                 approval: the last approval is what makes it run
  -y, --yes      approve every guard in every batch, beyond what the batch
                 approves (the swimmer's say-so: logged as "approved by swim
                 watch -y (<user>)", and in the consumed entry as approved_by).
                 The batch's own --approve and -y keep their credit.
  --interval D   how often to check the queue and for idleness (default 2s)
  --batches N    run up to N batches at once (default watch_batches, 4);
                 --batches 1 runs one at a time, on the screen.
  --no-tui       print what the swimmer waits for instead of the jobs screen
  On a terminal, while it waits, the watch shows every lane's job and the
  queue, with the batches running: ↑ ↓ (or k j) pick one, r runs it now
  and enter follows its output (esc comes back). q adds a job to the next
  batch (q again takes it out) and s submits the batch, as swim queue add
  N M does: one run, so After: between its lanes holds; x clears it.
    enter on a job follows its lane's log while it runs, in a batch or not;
                   v switches to its batch's output. When it ends, a header
                   bar says how: PASSED (green), FAILED (red, with the
                   failed steps) or INTERRUPTED, with the exit code and
                   time; enter on a batch does the same for the batch
    r on a job     runs it as swim run N does: you're at the terminal, so its
                   guards may ask
    q on a job     queues it as swim queue add N does (no guard approved: its
                   guards dry-run unless standing approvals cover them); the
                   swimmer takes it when its lanes are free
    r on a batch   starts a waiting batch now, out of turn, if its lanes are
                   free, with its own approvals (and -y's), exactly as in turn
    enter on a batch  follows its output (.swim/queue/out/<seq>-<id>.log);
                   v switches to the verbose output (its lanes' logs, the
                   running step's output included) and back
    l on a job     its lane's current log, every round (v: the last one),
                   running or not; followed while it runs
    l on a batch   its lanes' latest rounds, from their last run while it
                   waits, as they go while it runs (v: the batch's output)
    o              hide or show the running output: under the queue, the
                   latest lines of every running lane's log, the running
                   step's output included (shown by default)
  [DRY] marks a lane whose round dry-ran a guard, and a waiting batch with
  a guard neither it nor -y approves (signed approvals may still cover it).
  The screen stays up while batches run; their output goes to that file.
  With --batches 1 a batch runs in the interactive view instead, as swim
  run does. SWIM_TUI=0, CI and --once turn the screen off: then every
  batch's lines go to the watch's output as they come. In the screen,
  Ctrl-C quits the watch.
  Ctrl-C: the usual graceful stop of every batch in hand (lanes stop at a
  step boundary; again to force quit), each consumed as interrupted, then
  the watch exits; between batches, it exits at once. Batches still
  waiting stay queued.
  With no one watching: run it in tmux, or under a service manager of your
  own with --no-tui; swim watch --once (from cron, say) runs what's queued
  and exits.
`,
	"cache": `swim cache [ls] [--yaml]
swim cache rm PRESET ... | --all [--yes]
swim cache prune [--older D] [--max SIZE]
swim cache path [PRESET] [--ci]

  The tool caches this repo's lanes keep between rounds. A lane declares
  one in its header, with the lockfile the tool checks the cache against:
      # Cache: terraform @infra/orders/.terraform.lock.hcl
  and swim points the tool's own download cache (TF_PLUGIN_CACHE_DIR,
  npm_config_cache, PIP_CACHE_DIR, GOMODCACHE) at the repo's directory for
  that preset. See CACHE in swim guide.
  ls       each preset's cache: size, when it was last used and by which
           lanes, whether a running lane holds it. --yaml: swim.cache/v1
  rm       delete a preset's cache (asks at a terminal; --yes doesn't). A
           cache a running lane holds isn't removed. The tools download again
           what they need
  prune    delete the caches last used more than D ago (--older 30d), then
           trim to --max SIZE (default cache_max, 10GB), least recently used
           first; never one a running lane holds
  path     the repo's cache directory, or a preset's. --ci prints dir= and a
           <preset>_key= line per preset the lanes use (from their lockfiles),
           for a CI cache step around swim ci:
               - run: swim cache path --ci >> "$GITHUB_OUTPUT"
                 id: swimcache
               - uses: actions/cache@v4
                 with: {path: "${{ steps.swimcache.outputs.dir }}",
                        key: "${{ steps.swimcache.outputs.terraform_key }}"}
  Settings (config.yml): cache_dir (default ~/.cache/swim; under /tmp it
  gets your user id), cache_max, cache_slug (the repo's directory name).
  cache_dir and cache_slug aren't allowed in a repo's swim.yml.
`,
	"wait": `swim wait [--interval D] [--timeout D]

  Block until swim is idle and nothing new is about to start: no lane
  running, no swim run waiting on lanes, and no batch queued or running
  (an orphaned batch never runs, so it doesn't count). Prints what it is
  waiting on whenever that changes. Use it in scripts to wait out the
  current work and the queue behind it; swim queue add --wait does the
  same after queueing. With batches queued and no swimmer up, it waits
  until one starts (swim watch).
  --interval D   how often to check (default 2s)
  --timeout D    give up after D (exit 1, saying what is still busy)
`,
	"approve": `swim approve                                    list approvals and standing approvals
swim approve N|JOB [--guards A,B | --all] [--expires D] [--note T] [--key K]
swim approve --auto USER [--guards A,B | --all] [--expires D] [--key K]

  How approvers act. Every approval is an SSH-signed event in a committed
  file under approvals/, so an approver anywhere (with the repo) can add
  theirs and push it; the files are hash-linked logs of approve and revoke
  events, so their order is attested like commits.
  swim approve N   approve this exact lane's execution manifest: the script
                   and every file it sources or imports, its sidecar, how it
                   runs, swim.yml's policy keys and lookup.yml's rules (it
                   lists them, and the file records them). Its guards
                   (--guards, or --all) approve without a prompt or flag when
                   the approvers, named by lookup.yml, meet lookup.yml's rule
                   for each guard. Another approver runs the same command to
                   co-approve (the statement can't change). Writes
                   approvals/lane.N.<sha12>.m<manifest8>.signature. If
                   anything it pins changes it no longer matches (swim approve
                   lists it as "stale: <why>"): approve the new version. An
                   approval from before manifests names the script alone and
                   counts while the script matches.
  evidence: true   a lookup.yml rule that says evidence: true (from when swim
                   bound approvals to a round's evidence) approves nothing:
                   swim approve --guards refuses its guards, and at run time
                   no signed approval, flag, prompt, -y or queued approval
                   counts for them (they dry-run) until the rule drops it.
  --auto USER      a standing approval: every job USER queues (swim queue add,
                   signed with USER's key) is approved, by you, for the guards
                   given (default every guard). It is never consumed: it
                   stands until it expires (30d unless --expires says, never
                   longer than lookup.yml's standing.max) or you revoke it.
                   It binds lookup.yml's rule for each guard: once a guard's
                   rule changes (stricter or looser), it stops counting
                   there. Writes approvals/auto.USER.signature; approving
                   again after it stopped counting starts a new one.
  Approvals of a script and standing approvals for its queuer count
  together toward a rule (zach standing for bob + alice approving the script
  = 2 of platform-admin).
  --expires D  a new approval stops counting after D (24h, 90m, 30d).
  Listed, each approval shows its state: active, expired, revoked,
  stale: <why> (what it approves changed) or insufficient: <why> (its
  signers don't meet the rule, aren't in lookup.yml, or it outlasts
  standing.max). Who is who (users, keys, groups) is read live: a key
  removed from lookup.yml stops counting at once. A standing approval from
  before they expired keeps counting, with a warning (here, swim doctor and
  swim verify), until lookup.yml sets standing.max.
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
	"verify": `swim verify [--strict] [--allow-unsigned] [--levels] [--trust-pin SHA256] [--against DEST] [--yaml]

  Prove the chain of custody (.swim/chain.jsonl). It prints one answer,
  VERIFIED or NOT VERIFIED (exit 1), with what failed or what it couldn't
  check. Every entry must be signed: an unsigned one fails (nothing
  vouches for it, and a chain rewritten with no key looks the same) unless
  --allow-unsigned. Under it are four levels (--levels prints each: yes,
  no, partial, n/a, and what it rests on); any one saying no fails:
    consistent  every entry follows the one before it (sequence and
                previous-hash: a changed, removed or reordered entry breaks
                every link after it); every signature is valid for the key
                recorded beside it; every round's log section still hashes
                to what was recorded, the copy of its script kept under
                .swim/scripts/<sha256>.sh is unchanged, and its staged copy
                (.swim/runs/<run>/<N>/, until swim doctor --fix prunes it)
                still matches the manifest it ran; every signed approval's
                signatures check out. A chain replaced wholesale and
                re-signed with a new key is consistent too: that's what
                trusted is for.
    trusted     every signature is by a key in trust.yml, as pinned outside
                the repo (swim keys --help): pinned on first use, or with
                --trust-pin. partial: some entries are unsigned. n/a: no
                trust.yml.
    anchored    the chain still holds the entry every checkpoint at each
                anchor (chain_anchors) recorded, oldest to newest: entries
                removed from its end, or a chain replaced or rewritten,
                after a checkpoint left the machine, fail it, whatever
                checkpoints were sent since. A checkpoint with a
                bad signature, or signed by a key outside trust.yml, is
                ignored with a warning. Says nothing about entries after
                the checkpoint. n/a: no anchors, or no checkpoint yet.
    authorized  every approval recorded met lookup.yml's rule for its
                guard: a signed approval's approvers, named by lookup.yml,
                are enough of the right group; a prompt, flag, -y or queue
                approval only fails a rule with signed: true (swim keys
                --help). A failure under the lookup.yml the approval was
                recorded under (lookup_sha256) says no; under a lookup.yml
                that changed since, it's a warning (partial). n/a: no
                lookup.yml, or no approvals.
  Above the levels: each approvals file under approvals/ checked event by
  event (and whether a script approval is stale: the script, or something
  else its manifest pins, changed since), then notes, warnings and what's
  BROKEN.
  Exits 1 when a level says no, anything is BROKEN or an entry is
  unsigned (without --allow-unsigned); --strict also on n/a, partial and
  any warning.
  --trust-pin SHA256   check trust.yml against this hash instead of the pin
                       in ~/.config/swim/trust-pins.yml (CI, which keeps no
                       pins; swim keys pin prints it). Also SWIM_TRUST_PIN.
  --against DEST       anchor to DEST (file:/path) instead of
                       chain_anchors: its checkpoints, and the entries
                       swim chain export copied there must be the chain's
                       first ones (so a lost or wiped repo can be checked).
  --yaml               the result as data (schema swim.verify/v1): each
                       level a field of its own (consistent: "yes"), so CI
                       can require any of them; ok is what the exit code says.
`,
	"signoff": `swim signoff verify FILE ...

  A lane with # Signoff: CONTEXT is local CI: when its round passes, swim
  (not the lane) signs a statement with the key of whoever runs the
  launcher, swim run or the swimmer under swim watch: the commit and its
  tree, the lane, its script and manifest hashes, the job and run. It
  goes in .swim/signoffs/COMMIT/N.json, and a success commit status named
  CONTEXT goes to GitHub through their gh, which branch protection can
  require. Approving the script approves the signoff. Only when the tree
  was HEAD's as the run started and still is (no changed or untracked file
  git doesn't ignore) and the lane script is committed; push the commit
  first. A lane that can't be signed off says why; the run's result
  doesn't change.
  A signoff is an attestation, not a proof: it shows whose key vouches
  that the lane passed on that tree, not that the round ran honestly. Keep
  CI running on main (or on a sample) to catch a machine that differs.
  verify checks each file's signature, that the key is someone's
  (lookup.yml, or trust.yml), and that its commit (if this repo has it)
  still has the tree it names; exits 1 if any fails.
`,
	"history": `swim history [RUN|N|JOB] [--oneline] [-n N | -N] [--reverse] [--no-pager] [--yaml]

  The chain of custody, read like git log: newest first, each entry as
  "entry <hash> (#seq, run, lane, result)", its signer and date, then what
  happened: each approval (guard, approvers by name from lookup.yml, and
  how: signature, prompt, flag, auto, queue; the action and script) and
  each round (result, exit, job, script and log hashes, approvals used).
  --oneline      one line per entry: its short hash and what happened
  -n N, -N       only the newest N entries (--max-count N)
  --reverse      oldest first (after -n, as git log)
  --no-pager     on a terminal the output goes through $SWIM_PAGER or
                 $PAGER (default less, with LESS=FRX as git); this skips it
  --yaml         the entries as data, oldest first unless --reverse is
                 given (schema swim.history/v1)
  swim verify checks the chain.
`,
	"chain": `swim chain repair
swim chain checkpoint [--to DEST]
swim chain export --to DEST

  Look after the chain of custody file itself (.swim/chain.jsonl).
  repair       remove a half-written final entry: a write cut short by a
               crash, a kill or a full disk leaves a last line with no
               newline, and swim refuses to append after it (and swim verify
               reports it). repair, under the chain's lock, moves those
               bytes to .swim/chain.tail-<time>.bak, truncates the chain
               after its last complete entry, and appends a signed repair
               entry recording how many bytes were removed and their
               SHA-256. Complete entries are never touched; with nothing to
               repair it changes nothing. swim history shows the repair
               entry, and swim verify notes it.
  checkpoint   sign the chain's head now ({v, seq, head, at, host,
               runner_key_fp}, with the runner's key) and send it to each
               chain_anchors destination (and --to DEST). swim run, swim
               all, swim ci and each swim watch batch do this as they end
               (not swim run inside a lane). An anchor refuses a checkpoint
               that doesn't go past its last. A failed send is a warning; the
               next checkpoint is sent anyway, and swim doctor lists the
               anchor until then (checkpoint-unsent). Exits 1 when an anchor
               didn't take it. Also kept in .swim/checkpoints.jsonl, which
               proves nothing (a lane can rewrite it).
  export       copy the entries DEST doesn't hold yet, then a checkpoint.
               What DEST holds must be the chain's first entries, line for
               line; if the chain lost entries or was replaced since, nothing
               is written. swim verify --against DEST checks against it.
  Destinations (chain_anchors in swim.yml or config.yml; they add up):
    file:/path   a directory outside the repo: checkpoints.jsonl and
                 chain.jsonl, only ever appended to (put it on another
                 volume, mounted append-only, or held by someone else)
  An anchor proves nothing about entries written after its last checkpoint,
  and someone holding the runner key and write access to every anchor can
  rewrite everything: keep anchors out of the operator's reach.
`,
	"keys": `swim keys
swim keys add NAME [KEY | FILE.pub]
swim keys trust [KEY | FILE.pub] [--remove] [--sign] [--key K]
swim keys pin

  Who's who: lookup.yml's users with their key fingerprints, groups (with
  nested groups expanded), require rules, trust.yml and its pin, and the key
  this machine signs with and whom it maps to.
  add lists a key for NAME in lookup.yml (default: the key this machine
  signs with), creating the file; it keeps the rest, comments included.
  A repo's first swim approve does it for you ($USER) when there's no
  lookup.yml, since an unlisted key's approval counts for nothing.
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
          signed: true                   # only signed approvals meet it
        - group: ops                     # no guards: every other guard
  With no rule a signature from any listed user approves; with no
  lookup.yml signatures don't count at all (their keys can't be named).
  signed: true makes a prompt, FLAG=1, -y or queue approval of the rule's
  guards fail swim verify's authorized level (the guard still accepts it:
  the operator's call at the time, recorded as such).

  trust.yml (repo root, committed) lists the keys swim verify trusts to
  sign the chain: the runners' and the approvers'. lookup.yml's keys must
  be among them (swim lint). Its key set's SHA-256 is pinned outside the
  repo, per repo, in ~/.config/swim/trust-pins.yml: the first swim verify
  pins it (trust on first use), or swim keys pin. After that a change
  counts only when signed by a key the pinned set trusts; it's recorded in
  trust.yml.sig (committed), and each operator's next swim verify moves
  their pin along. An unsigned change makes verify's trusted level fail
  until that operator runs swim keys pin again.
  trust     add KEY (a public key line or a .pub file; default this
            machine's signing key) to trust.yml, creating it; --remove
            takes it out. --sign records the change in trust.yml.sig,
            signed with --key K (default signing_key), a key trust.yml
            already lists. A key removed this way still verifies what it
            signed before.
  pin       pin trust.yml as it is now (after checking its keys), and print
            the hash for CI (swim verify --trust-pin). Also forgets keys
            earlier pins kept for what they signed before.
  The pins file is the operator's: a lane can't change it through the
  repo, but runs as the same user, so it could write it directly. For a
  pin a lane can't reach, give CI --trust-pin from its own settings.
`,
	"interrupt": `swim interrupt N|JOB

  Ask one running lane to stop at its next step boundary, from any
  terminal: its current step finishes, no further step starts, and the round
  ends interrupted (STOP  interrupted by operator (after: <step>), exit 130).
  Other lanes keep running. The same thing the first Ctrl-C does to every
  lane of a swim run.
`,
	"claim": `swim claim N [--for D]
swim claim N --release [--force]
swim claim N --break "<reason>"

  A claim is a lease a session holds on a lane while it writes the lane's
  round: swim new takes one (claim_lease, default 2h), swim lint refreshes
  it, and while it's live another session's swim new, stub and archive
  refuse the lane, naming who holds it. It ends when the lane starts
  running (swim run, all, watch: a run warns if another session held it,
  and runs anyway), when it's archived or stubbed, when it's released, or
  when the lease runs out. swim status shows it.
  swim claim N takes or refreshes it explicitly (--for D); --release gives
  it up (--force: another session's); --break takes over a live claim with
  a reason, and --force on new, stub or archive does too: both are logged
  as claim-broken. A session is SWIM_SESSION, else user@host: set
  SWIM_SESSION per session, or two sessions of one user on one machine are
  one. Claims are advisory: they guard swim's commands, not an editor
  writing lane.N.sh, and stay in this checkout's .swim/claims/.
`,
	"lint": `swim lint [N|JOB ...] [--strict] [--yaml] [--fix]

  Check lane scripts before they run; changes nothing (but --fix). With no lanes, lints
  every lane script (stubs skipped). Each finding is printed as
      lane.2.sh:5  error  <what is wrong>  [code]
                          fix: <how to fix it>
  then a summary line ("2 errors, 1 warning"). Exit 1 on any error; with
  --strict (for CI), on warnings too. --yaml prints a swim.lint/v1 document.
  --fix adds the header lines a script's body needs, for you to review:
  # Guards: for each guard it uses without declaring (with the guard's own
  action as the description), # Guards: * for a flag it names at run time,
  # Includes: for a repo file it sources or imports. It never removes or
  rewrites a line, and skips running lanes and lanes of jobs.

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
    guard-unlisted (error) a guard flag used but not declared in # Guards:
                           (refused when it asks; a warning with # Guards: *)
    guard-dynamic (error)  guard "$FLAG" without # Guards: *
    guard-unused (warn)    a # Guards: flag no guard uses
    destructive (warn)     delete/destroy/drop/rm -r outside an if guard block
    blocked (error)        git push/commit/pull or a blocked_commands pattern
    secret-echo (warn)     echo/printf of a secret variable
    header (warn)          a Timeout, Step-Timeout or Locks value swim ignores
    env (error)            the lane's env files don't parse, or set its guard flag
    fail (error)           Fail: without syntax 3
    removed (error)        what this swim no longer has: Matrix:, Always:,
                           Finally:, Rerun-Children:, an outcome handler
                           (on_success, on_failure, on_exit), modules
                           (# Modules:, mod, mod_env, an SDK's module call),
                           the secret store (swim secret, an SDK's
                           secret.get), or evidence steps (use snapshot)
    outputs (warn)         an output whose name suggests secrets (output files
                           aren't masked)
    cache (error)          a # Cache: with an unknown preset or no lockfile, a
                           lockfile that isn't there, or pip requirements
                           without --hash entries (swim run refuses the lane)
    cache-bypass (warn)    npm install, pip install without --require-hashes or
                           TF_PLUGIN_CACHE_MAY_BREAK_DEPENDENCY_LOCK_FILE in a
                           lane that caches them: the cache isn't checked
    includes (error)       a Python/TypeScript lane imports a local module its
                           # Includes: doesn't list (it isn't staged: the
                           import fails under swim run)
    source (error)         a bash lane sources a local file its # Includes:
                           doesn't list (the round stops there under swim
                           run), or a path built at run time (source "$x"),
                           which swim can't pin or stage
    observe (warn)         an # Observe: probe that looks like it writes or
                           changes something (an error if it's blocked)
    check-unsafe (warn)    a destructive-looking command (terraform apply, a
                           delete) before stage change, in a stage that
                           should only read
    input-dep (error)      input N of a lane not in After: (or config deps)
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
    lanes-setting (warn)      a lanes: setting, which swim ignores now
    gitignore (warn)          swim's .gitignore block missing or out of date
    tracked (warn)            git tracks lane scripts or .swim/
    stale-pid (warn)          .swim/laneN.pid naming a process that is gone
    toolchain (error)         config toolchain fails in bash -c (30s limit)
    git (error)               git missing, or not inside a git repo
    header-env-secret (warn)  header_env lists a secret variable
    lock (info/warn)          .swim.lock missing or in the old form (info),
                              invalid or pinning other syntaxes (warn)
    env-file (error)          an env file that doesn't parse, or sets a
                              lane's guard flag
    outputs-old (info)        round outputs older than outputs_keep (default
                              14d) that no lane's latest round uses; --fix
                              removes them
    anchors-none (info)       the repo has a chain but no chain_anchors
    checkpoint-unsent (warn)  a chain anchor hasn't had the latest checkpoint
    trust-unpinned (warn/error) trust.yml not pinned on this machine yet
                              (warn), or changed since the pin with no
                              signed change (error): swim keys --help
    stages-old (info)         runs' staged lane copies (.swim/runs/<run>/)
                              older than outputs_keep, of no lane's latest
                              round; --fix removes them
    evidence-rule (warn)      a lookup.yml rule says evidence: true: nothing
                              can approve its guards (they dry-run)
    standing-approval (warn)  a standing approval still counting under the
                              old terms: it never expires (until lookup.yml
                              sets standing.max), or a lookup.yml rule
                              change doesn't stale it
    cache-dir (error)         the cache directory is a link, isn't yours, or
                              others can write it: lanes with # Cache: won't
                              start
    cache-unused (info)       tool caches unused for 30 days, or the repo's
                              caches over cache_max
    remote-running (warn)     a lane has run on another machine sharing the
                              repo for over a day; if that machine is gone,
                              release it (rm .swim/laneN.pid, then swim
                              status --rebuild)
    removed-setting (warn)    a setting swim no longer has (chime,
                              schedule_tz, observe, modules, secrets,
                              ambient_credentials, secrets_key, headless,
                              hub, pause_warn): it's ignored; delete it
    signing-key (info/warn)   where the runner key lives: warns when it's
                              a file with no passphrase (a lane could read
                              it and sign as the runner); notes that a key
                              in ssh-agent can be asked by a lane unless
                              added with ssh-add -c

  --fix applies only safe local repairs, then checks again: it rewrites
  swim's .gitignore block and removes stale pid files. It never edits
  config, lane scripts or git.
`,
	"changelog": `swim changelog [-n N] [--since VERSION] [--all]

  What changed in swim, from the changelog built into this binary (the
  repo's CHANGELOG.md when it was built). Run it after updating to see what
  is new. Works anywhere; it needs no repo.

  swim changelog                  the newest revision (default -n 1)
  swim changelog -n 3             the newest three revisions
  swim changelog --since 0.20261014.1
                                  every revision after that version (or tag,
                                  v0.20261014.1; an older 7.20261013; or a
                                  pre-release commit id)
  swim changelog --all            every revision

  A revision is one "## " section: Unreleased (on a build from main), a
  release version 0.<YYYYMMDD>.<index> (earlier, <breaking>.<YYYYMMDD>), or
  a commit for revisions before versioning. A revision that changes a syntax
  version has Upgrading notes: read them, and swim lock --help, before
  running lanes.
`,
	"skill": `swim skill

  How to install the Claude skill for swim: where it lives (skills/swim in
  swim's repo, at this binary's release tag, else main) and the line to
  paste into a Claude Code session to install it. The skill teaches Claude
  to read this guide, write and lint lanes, hand runs and approvals to the
  operator, and read results back. Works anywhere; it needs no repo.
`,
	"version": `swim version

  swim's version (as swim --version prints it), then the newest release
  tag in swim's repo and how this build stands against it. It asks with
  git ls-remote, so your own git credentials apply (https, then ssh; it
  never prompts and gives up after a few seconds). When it can't reach the
  repo it says why. It always exits 0. swim --version prints only the
  first line and never touches the network. SWIM_RELEASES_URL names
  another remote (a fork or a mirror). It also asks the Go module proxy
  (GOPROXY's first http(s) entry) what @latest is: the proxy caches it, so
  for a while after a release go install ...@latest still installs the one
  before. Then it says so, with the line that installs the tag by name.

      swim 0.20261014.1 (64a0e07)
      latest release: v0.20261015.2, newer than this swim
        update: go install github.com/gates-brightly/swimlane/cmd/swim@v0.20261015.2
        then:   swim changelog --since 0.20261014.1
`,
	"lock": `swim lock [--upgrade]

  swim's version is 0.<YYYYMMDD>.<index> (swim --version), e.g. 0.20261014.2:
  the release's date and its number that day; a build between releases is
  0.<YYYYMMDD>. The version says nothing about compatibility: syntax
  versions do. Lane scripts (# swim: syntax 4) and logs (# swim lane log |
  syntax 2) say theirs in the file; swim's state files (status, the chain,
  queued batches, approvals) have one too, which they don't record.

  .swim.lock, at the repo root, pins the syntax versions a repo's lanes use:
  script, log and state. swim writes it on the first run (swim run / swim
  all, or a lane script run directly) if there is none. Commit it, so
  everyone runs a compatible swim. Releases that change no syntax never
  touch it.

  When the lock and this swim disagree, swim refuses to run lanes or write
  new ones (swim status / log still work, with a warning):
    - lock is newer than this swim: update swim, then run again:
        go install github.com/gates-brightly/swimlane/cmd/swim@latest
        (or in a swimlane checkout: git pull && just install)
    - lock is older than this swim: the repo was set up before a syntax
      changed. Read swim changelog for what changed, update the lane scripts
      (swim new writes the current template), let running lanes finish, then
      record that the repo is ready with: swim lock --upgrade
      (it warns about a swimmer up now that reads other syntaxes: that
      swimmer leaves batches queued until it's updated and restarted)

  swim lock             show this swim's version and syntaxes, and the repo's lock
  swim lock --upgrade   move the lock up to this swim's syntaxes (and rewrite
                        an old-form lock)

  Syntax versions:
    script 4, log 2, state 2   once modules (# Modules:, mod, mod_env), which
                               swim no longer has: syntax 4 now adds nothing
                               over 3, and swim new still writes it
    script 3, log 2, state 2   execution manifests: chain entries, signed
                               approvals and queued batches name the
                               manifest they ran or approved
                               (manifest_sha256), and approve entries the
                               lookup.yml in force (lookup_sha256), which
                               a swim of state 1 can't verify; approvals
                               signed by a state 1
                               swim still count (script hash alone)
    script 3, log 2, state 1   since 0.20261014.1; the formats of swim 7.20261013

  Before 0.<date>.<index> the version was <breaking>.<YYYYMMDD> and the lock
  pinned that breaking number. A lock that says breaking: 7 means script 3,
  log 2, state 1: swim lock --upgrade once everyone runs a swim with state
  2; an earlier one is older: swim lock --upgrade after updating the lane
  scripts.
`,
	"help": `swim help [command]

  Print the overview (the full guide when stdout isn't a terminal), or one
  command's detail.
`,
	"guide": `swim guide

  Print the full guide: how planners, workers and operators use swim, every
  concept, and every command's detail. swim --help prints it too when its
  output isn't a terminal, so an agent reading swim --help gets all of it.
`,
}

// For is a command's help.
func For(name string) (string, bool) {
	h, ok := Commands[name]
	return h, ok
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
// FullOrder is the order of the command details in swim guide; every
// command in Commands is in it (help_test checks).
var FullOrder = []string{"init", "config", "env", "cache", "new", "run", "all", "status", "log", "report", "step", "lib",
	"gate", "snapshot", "guard", "stage", "drift", "stop", "msg", "input", "output",
	"archive", "clean", "stub", "note", "lock", "changelog", "version", "skill", "interrupt", "claim", "queue", "watch", "wait", "approve", "revoke",
	"verify", "history", "chain", "keys", "ci", "signoff", "lint", "doctor", "guide"}

// Short is swim --help on a terminal: the commands most people need, one
// line each, and where the rest is.
func Short() string {
	return `swim: guarded ops scripts with signed approvals

  Your AI writes the ops change. You sign the exact bytes. swim runs it
  and gives you a receipt anyone can verify.

USAGE
  swim <command> [flags] [args]
  swim N|JOB ...               shorthand for swim run N|JOB ...
  swim <command> --help        one command's detail
  swim guide                   the full manual (for people and AI agents)

CORE
  init                         set up this repo
  new TASK "<round goal>"      write a lane script named TASK (or new N: lane N)
  run [TASK ...]               run lanes: guarded steps are dry runs until approved
  approve N --guards A         sign this exact script, its inputs and policy
  revoke N                     withdraw your approval
  status [N]                   last state of every lane
  log [N]                      a lane's log, step by step
  verify                       prove the chain of custody
  history [--oneline]          who approved and ran what

UNATTENDED
  queue add N ...              queue a batch to run once its lanes are free
  watch                        run queued batches

MORE
  lint, doctor                 check lane scripts and the environment
  keys, chain                  who's who; chain repair and anchoring
  env, cache                   what lanes get to work with
  archive, clean, stub, note   tidy up and record
  interrupt, wait, claim       control running lanes
  report                       what changed since you last looked
  signoff verify FILE          check a signoff (# Signoff: posts a commit status)
  ci, config, lock, changelog, version, skill

  Lane scripts call: gate, guard, stage, run, snapshot, drift,
  stop, msg, input, output (swim guide: WRITING A LANE SCRIPT).

  swim guide prints everything, including each command's detail.
`
}

func Full() string {
	s := Guide + "\nCOMMAND DETAIL\n"
	for _, order := range FullOrder {
		s += "\n" + Commands[order]
	}
	return s
}
