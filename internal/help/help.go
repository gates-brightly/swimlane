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
    - adds a marked block to .gitignore: lane.[0-9]*, .lane*.rc, .env.swim, .swim/,
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
    chime        true | false | failure   chime when swim run/all finishes
                 (failure: only when a lane failed, was skipped or the run
                 was interrupted). Default false.
    chime_style  bell | sound | notify    bell rings the terminal bell
                 (twice on failure); sound adds a system sound, notify a
                 desktop notification, falling back to the bell. Default bell.
    chime_min_s  seconds (0 or more)      runs shorter than this never
                 chime. Default 10.
    outputs_keep duration (14d, 36h)      round outputs older than this that no
                 lane's latest round uses are pruned by swim doctor --fix.
    msg_log_interval  duration            at most one MSG log line per lane per
                 this (default 10s; 0 logs every change of swim msg).
    queue_lease  duration (10s or more)   a queue worker renews its claim's
                 heartbeat every third of this while it runs a batch; seen
                 from another host, a claim with an older heartbeat is
                 orphaned. Default 2m.
    pause_warn   duration (1m or more)    swim doctor warns about a lane paused
                 (swim pause) longer than this. Default 4h.
    claim_lease  duration (1m or more)    how long a lane claim (swim new,
                 swim claim) lasts unless refreshed. Default 2h.
    watch_batches  a number (1 or more)   how many queued batches one swim
                 watch runs at once (default 4; swim watch --batches N).
    schedule_tz  an IANA zone name       the zone swim schedule reads times
                 in (default: this machine's), e.g. America/Chicago.
    finally_timeout  duration             once a run is stopped, how long its
                 finally lanes (# Finally:) still get, in all (default 10m).
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
  ($XDG_CONFIG_HOME/swim/config.yml if set). Workers must not edit it; give
  the operator exact YAML lines instead.
`,
	"new": `swim new N "<round goal>" [--lang sh|py|js|ts] [--job ID] [--force]

  Write lane.N.sh (or lane.N.py, .js, .ts with --lang) from the template: the goal on its Round: line, a new job
  id on its Job: line, the toolchain preamble from config, and example
  snapshot/gate/guard/verify steps to replace. Prints the job id. Marks the
  lane's pending round and job in status.yml.
  Refuses if lane N is running, or already holds a pending round (use
  --force to replace a pending round you own).
  --job ID   use your own job id instead of a random UUID: 8-64 letters,
             digits, '.', '_' or '-', not all digits, unique across lanes.
  --lang L   sh (default), py, js or ts. The lane's script in another
             language, and its lane.N.yml, are replaced. A ts template has
             no #! line: swim asks which runner once (see OTHER LANGUAGES).
`,
	"run": `swim run [N|JOB ...] [--rerun] [--plain] [--no-tui] [--run-id ID] [--parallel N]
         [--chime|--no-chime] [--yaml] [--yaml-output] [--interrupt graceful|immediate] [-y|--no-prompt] [--replace]
swim run [options] "<label>" -- cmd [args...]     (inside a lane: one step; see OTHER LANGUAGES)
swim run 'N[cell]' ... | swim run N --failed     (matrix rounds: one cell; only the cells that didn't pass)
swim run N.K[.K...] ...                          (a job, e.g. 1.2 or 1.5.9.9: it and the jobs under it)

  Run lanes. Each argument is a lane number or a job id (full, or 8+
  characters of it). A job id pins the run to that job: it runs the lane
  whose script holds it, and refuses if none does. Named lanes always run,
  even if their round already passed (rounds are rerunnable).
  With no arguments (or "swim all"), runs every lane whose lane.N.sh holds a
  pending round (a Round: line, not a stub) that hasn't passed yet; rounds
  that passed show as "done". --rerun runs them too. Always lanes
  (# Always: yes) never count as passed: they run every time, first, and
  every other lane waits for them to finish (whatever their result); a
  named lane pulls in the Always lanes it names in After:.
  Lanes run in parallel as "bash lane.N.sh" from the repo root.
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
    - A lane paused by swim pause holds a live round: swim run N on it asks
      (at a terminal, default no) to end that round as interrupted and run
      the lane's new job; without a terminal, --replace does it, and nothing
      else does. On the same job: swim unpause N resumes it, --replace
      restarts it from the top.
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
                chain entry and swim history, swim ci's summary and the hub
  --no-prompt   never ask; an unset guard flag is a dry run
  --no-finally  leave out the finally lanes (# Finally:) naming the lanes
                run, which a run of named lanes otherwise pulls in
  Each lane runs a staged copy of its files (.swim/runs/<run>/<N>/), fixed
  before it waits for anything and checked again right before it starts;
  an edit to the working tree after that changes nothing in the run. See
  EXECUTION MANIFEST in swim --help.
`,
	"all": `swim all [--rerun] [--plain] [--no-tui] [--run-id ID] [--parallel N]
         [--chime|--no-chime] [--yaml] [--yaml-output] [--interrupt graceful|immediate] [-y|--no-prompt] [--replace]

  Run every lane whose pending round hasn't passed yet: the same as
  "swim run" with no lane numbers. Rounds that already passed are shown as
  "done" and not run again; failed, interrupted, skipped and new rounds run.
  Always lanes run every time, before the rest; with Rerun-Children: yes,
  so do the passed lanes downstream of one.
  --rerun   run every pending round, including ones that already passed.
  Every lane script counts, whatever its number. Dependencies ("# After:" lines and config deps), the live
  view and its keys (--no-tui), guard flags, the summary and the chime (--chime / --no-chime) all
  work as in "swim run".
  Stubbed lanes and lanes without a Round: line are left alone.
`,
	"plan": `swim plan [N|JOB ...] [--rerun] [--no-finally] [--yaml] [--check [--parallel N] [--timeout D]]

  Show what "swim all" (or "swim run N ...", with lanes) would do, without
  running or changing anything, Terraform style:
      [+]    run    a new job
      [~]    retry  the job ran before and didn't pass
      [+/-]  rerun  the job already passed and runs again (named lane, --rerun)
      [-]    skip   the job won't run (a dependency outside the run hasn't
                    passed, or one in the run won't run)
      [*]    always an Always lane: setup that runs every time, first
      [@]    scheduled  a scheduled batch (swim schedule) runs it at its
                    time; swim all leaves it, swim run N refuses it
      [=]    held   depends on a scheduled lane: it waits for that batch
                    (run now, it would skip)
  A finally lane (# Finally:) is marked [F] with the lanes it waits for to
  end, however they end.
  Always lanes come first; a passed lane that a Rerun-Children lane reruns
  shows "rerun (swim N)"; "then, once swim N finish" starts the rest.
  The jobs form a tree: top-level lanes start at once, and each lane sits
  under the lanes it waits for ("# After:" and config deps); a lane waiting
  for several shows "wait [2,3]" and appears in full once,
  "(shown above)" elsewhere. Each job's guard flags are listed as set
  (approved) or unset (dry run); lanes running right now are flagged.
  Ends with "Plan: N to run, N to retry, N to rerun, N to skip." Jobs with
  nothing left to do are summarised as "N items have completed with no
  remaining work" ("No changes." when that is everything), followed by the
  idle lanes in use.
  Fails, like swim run would, on a dependency cycle or an "# After:" that
  doesn't resolve.
  --yaml   the plan as data (schema swim.plan/v1): each lane's action, what it
           waits on, why it would be skipped, its guards, timeout and locks,
           and its last check.
  --check  a read-only preview: first run each lane's snapshot and check
           stages for real, all at once (After: ignored, Locks: kept), and
           stop every round where change would begin (CHECKED). Guards never
           approve (not even with the flag set), handlers don't run, logs go
           to .swim/logs/agentN.check.log and lane status is untouched (only
           check_result, check_at, check_sha256, check_stop, check_guards). A
           round with no stages, or a compiled lane, is refused. The plan
           then shows each lane's check: check PASS, stops at gate "<label>",
           check ~ (it depends on lanes whose changes haven't run), and the
           guarded changes; exits 1 if any check didn't pass. A plain swim
           plan shows the last checks too, marked stale once a script changes.
           --parallel N caps checks at once; --timeout D caps the whole check.
`,
	"report": `swim report [N|JOB ...] [--earmark] [--name NAME] [--since T|RUN|JOB] [--no-logs] [--results] [--yaml]
swim report --earmark --wait [--timeout D] [--interval D]
swim report --earmarks | --earmark --reset [--name NAME]

  What changed since you last looked, in one call, untruncated: the lanes
  whose state or job changed (as swim status words them, with what they
  were, then one unchanged: line), held locks and the queue, every
  .swim.log line written since, and every lane-log line written since:
  the rest of the round that was in progress, then each round after it
  (cells' and group jobs' logs too), verbatim, as swim log --raw prints
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
                 history (lines), lanes: [{lane, cell, rounds:
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
  (dry_run), here, in the live view, the watch screen and the hub.
  --yaml     the status file's fields as YAML, tagged schema: swim.status/v1;
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
                command: see "TIME LIMITS" in swim --help)
  --retry N, --backoff D, --retry-on CODES, --no-retry-timeout
                retry on failure (see "WRITING A LANE SCRIPT" in swim --help)
  --expect-fail[=CODES]
                the command should fail: the step passes on a non-zero exit
                (one of CODES, e.g. =1,2, if given) and is logged "failed as
                expected: exit N"; exit 0 or another code fails it. A time
                limit or a signal still fails it. Not with --retry.
  Lane scripts call this through run/gate/snapshot; you rarely need it directly.
`,
	"gate": `swim gate [options] "<label>" -- cmd [args...]

  In a lane script in any language (see OTHER LANGUAGES in swim --help): run
  one step like swim run "<label>" -- cmd; if it fails, record STOP and end
  the round, stopping the program even if it carries on. Options as for
  swim run's in-lane form: --timeout D, --retry N, --backoff D,
  --retry-on CODES, --no-retry-timeout, --expect-fail[=CODES]. (In a bash
  lane, use the library's
  gate.)
`,
	"observe": `swim observe [RUN|N|JOB] [--yaml]
swim observe check FILE [RUN]
swim observe knowledge [--yaml]
swim observe accept ID [--note TEXT] | reject ID

  A run's observations: facts computed from what swim records, each with
  its numbers and the runs it cites. The last run's by default; for N or
  JOB, that lane's or job's in the last run it ran in. swim run computes
  them as it ends, into .swim/observations/<run>.yml (beside the logs,
  never in them), and swim report shows the ones of runs that ended since
  your earmark. They decide nothing: acting on one is a script change.
    timeout   a step hit its time limit, and how often it has
    retried   a step passed only after retries
    waited    a lane waited 2m+ for its lanes, its locks or a slot
    slow      a step took over 1.5x its p90 and 30s+ over its median (with 5+
              earlier runs of it)
    fast      a step took under a quarter of its median: did it skip its work?
    failing   a step failed in each of its last 3 runs
    dry       a guard ran as a dry run in each of its last 3 runs
    drift     the same DRIFT came back
    flaky     a target's steps needed retries or failed in 25%+ of its last
              runs (4 or more, across lanes): run --target T, # Targets: T
  A step's history is matched by its label, or its --key when it has one
  (so a relabelled step keeps it). swim status shows a lane's observations
  from its last run (observed: ...).
  The thresholds are swim.yml's observe: (slow_factor, slow_min_s,
  baseline_min, window, wait_min_s, failing_streak, dry_streak,
  flaky_share, flaky_min); they're
  not policy and stale nothing. --yaml: swim.observations/v1.
  Each has an id (slow-2-3). A finally lane gets those of the lanes it
  waited for in a file, SWIM_OBSERVATIONS, for a write-up (mod claude
  --input "$SWIM_OBSERVATIONS" ...). check lists the ids a write-up (FILE)
  cites that the run (the last one by default) doesn't have: a citation
  someone made up. Exit 1 on any.
  knowledge  what keeps being true: an observation of the same kind for the
             same lane and step in 3 or more of the lane's last runs (window,
             default 20) is proposed. accept copies one into knowledge/steps.yml
             (commit it), with who and when, and swim plan shows it beside the
             lane (known: ...); reject keeps it from being proposed again. An
             accepted entry not seen in the lane's last 5 runs is retired:
             knowledge lists it, and doctor (knowledge-retired). Knowledge
             decides nothing: acting on it is a script change.
`,
	"mod": `swim mod [--label L] [--gate|--evidence] [options] CMD [args...]

  In a lane program with no SDK (a script that runs swim run ... -- for
  its steps): run a command of a module the lane declares (# Modules:) as
  a step, as a bash lane's mod does. --gate: its failure ends the round;
  --evidence: its output is the round's evidence (tf plan). A gate module
  (gate: true in its module.yml) that fails ends the round. Step options
  as for swim run's in-lane form. There is no swim mod_env (a command
  can't set its caller's environment): use an SDK's module(..., env=True),
  or mod_env in a bash lane.
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
  now (exit 1); the program is stopped.
`,
	"env": `swim env [N|JOB] [--yaml]

  The environment env files give lane N (with no lane: the general and repo
  keys), masked as the lane's logs show it, with where each key comes from
  (file and line) and whether the shell overrides it. Exits 1 if a key is
  one of the lane's guard flags. --yaml: a swim.env/v1 document with the
  env_sha256 the round would record. See ENV FILES in swim --help.
`,
	"msg": `swim msg [--progress N/M] "<text>"

  In a lane (bash, any language, or a command inside a step): post the
  lane's status message, where a long job is now. The live view, swim
  status, swim run --yaml (msg events) and the hub show it; the latest
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
  macOS bash 3.2). See "WRITING A LANE SCRIPT" in swim --help for the functions.
`,
	"clean": `swim clean [--queue|--no-queue] [--scripts|--no-scripts] [--all] [--include-always] [--no-input] [-n] [-o yaml]
swim clean --restore STAMP

  A clean slate, recoverable: nothing is deleted. It archives every lane's
  finished rounds (passed, failed, interrupted, stale) under one name,
  clean-YYYYMMDD-HHMM, as swim archive does (their logs become
  agentN.prev-clean-....log; status shows them idle), all or nothing. It
  leaves running and waiting lanes, and Always lanes (--include-always
  cleans them too). Then it asks two questions, each defaulting to no:
    remove the waiting queue batches?   (a batch someone else queued and
        signed is asked about separately; orphans are only listed)
    move the lane scripts aside?        (lane.N.*, group files, lane.N.yml,
        to .swim/cleaned/<stamp>/ with their SHA-256s; never files they
        include, swim.yml, lookup.yml or approvals/)
  Without a terminal (CI, an agent, --no-input) it archives and answers no
  to both. Answer up front with --queue/--no-queue and --scripts/
  --no-scripts; --all is both. Not -y: that approves guards.
  -n, --dry-run   print the plan; change nothing
  -o yaml         the plan, or what it did, as swim.clean/v1
  --restore STAMP put a clean's scripts back (refused if a path was reused)
  Refused while swim is busy (a run or worker holds lanes). Recorded in
  .swim.log (clean, and archive/queue-rm per item) and the chain (a clean
  entry: the moved scripts' SHA-256s, the removed batches); swim verify
  still finds the archived logs. swim prune deletes what clean set aside.
`,
	"prune": `swim prune --older D [--logs] [--confirm] [-n]
swim prune --stamp STAMP [--confirm]
swim prune

  Delete leftovers for good, older than a cutoff you give: scripts swim
  clean moved aside (.swim/cleaned/), consumed queue entries
  (.swim/queue/done/), run stages (.swim/runs/<run>/, never the stage of a
  lane's latest round, which swim verify checks; run records stay) and,
  with --logs, archived logs (agentN.prev-*.log). Never the chain,
  .swim.log, approvals/, lookup.yml or kept script copies (.swim/scripts/).
  A bare swim prune only says what each category holds. --older D is a
  duration (30d, 12h); --stamp prunes one clean's moved scripts.
  At a terminal you type "prune" to confirm; without one, --confirm. -n
  prints the plan and deletes nothing. Refused while swim is busy.
  Before deleting an archived log it writes a prune entry in the chain
  with each of its rounds' hashes: swim verify then reports those rounds
  as pruned (when, by swim prune), not missing, and says it can't recheck
  them. swim doctor says when swim prune --older 30d would free over
  100 MB.
`,
	"trash": `swim trash N|JOB ... [--force] [-n]
swim trash --restore STAMP
swim trash

  Move one lane out of the way, recoverably: its script (any language) and
  its sidecar lane.N.yml go to .swim/cleaned/<stamp>-lane<N>/, where swim
  clean sets scripts aside, its current log is archived (trash-<stamp>),
  and the lane leaves swim status and swim watch. Nothing is deleted:
  --restore STAMP puts the files back (swim clean --restore takes the same
  stamps), its round pending again, and swim prune deletes the entry for
  good later. A bare swim trash lists the trashed lanes.
  Refused for a running or waiting lane (swim interrupt N first), a lane in
  a queued or scheduled batch (swim queue rm, swim schedule rm first: a
  batch is pinned to its script), a group file's lane (edit the file), and
  an Always lane unless --force. Files the script includes, swim.yml,
  lookup.yml and approvals/ are never touched; a signed approval of the
  script counts again once it's restored unchanged.
  Recorded in .swim.log (trash, untrash) and the chain with each moved
  file's SHA-256. swim watch's d (twice) and the hub's Trash button (with
  trash in swim.yml's headless.allow) do the same. -n says what would move.
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
  An Always lane is standing setup, not a round: it needs --force.
`,
	"stub": `swim stub N|JOB "<message>" [--force]

  Replace lane.N.sh with a "nothing pending" stub carrying <message>. The lane
  shows as idle and "swim run" skips it. Refuses if lane N is running, if a
  group file is the lane (edit or remove it there), and without
  --force if it is an Always lane (standing setup, not a round).
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
    swim log N          for a matrix lane: each cell's last result, then the cells
                        that didn't pass, each with its first failed step and the
                        first line its command printed
    swim log N --failures   only those cells
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
  --failures    a matrix lane's cells that didn't pass, and why (see above)
`,
	"ci": `swim ci [N|JOB ...] [--changed[=BASE]] [--rerun] [--junit FILE] [--provider github|gitlab|generic]
        [--require-work] [--heartbeat D] [--run-id ID] [--check]

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
  --rerun           also rounds that already passed. A test suite as rounds
                    doesn't need it: an Always lane with Rerun-Children: yes
                    at its root reruns the rest
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
  --check  a pull request preview: the snapshot and check stages only, read-only
           (see swim plan --check), every pending round.
`,
	"queue": `swim queue [list] [--all] [--yaml]
swim queue add [N|JOB ...] [--approve FLAG[,FLAG]]... [-y] [--parallel N] [--note TEXT] [--wait] [--timeout D] [--no-finally]
swim queue N|JOB ... [flags]     (shorthand for swim queue add)
swim queue show|rm|requeue ID

  Queue a batch of rounds to run as soon as its lanes are free, by a
  worker (swim watch). Get a batch ready, queue it with its approvals, and let it
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
            runs, and a guard flag in the worker's own environment does not
            count (the queue entry alone says what may change).
            A batch that approves none of its lanes' guards (and no
            standing approval of yours covers) warns on stderr, naming
            each lane and the guards that will dry-run.
            Workers say which swim they are (.swim/workers/): with workers
            up and none able to run the batch (its swim reads other
            syntaxes than .swim.lock pins, or older than a lane's script),
            add refuses, naming each worker's swim and this one; with no
            worker up it queues and warns. The entry records the swim that
            queued it (swim, syntax).
  list      waiting, running and orphaned entries, and the last 5 consumed
            (--all: every one); --yaml for scripts (schema swim.queue-list/v1).
            A batch a worker leaves queued says why ("left queued: ...")
  show ID   the entry file; ID is its id, a 4+ character prefix, or #seq
  rm ID     remove an entry that isn't running
  requeue ID  put an orphaned (its worker died) or consumed entry back at the
            end of the queue
  A claimed entry is orphaned when its worker is gone: on the worker's host,
  its process (pid and start time) is; from another host sharing the queue,
  its heartbeat is older than its lease (queue_lease, default 2m). A claim by
  an older swim (no heartbeat) can't be judged from another host and shows
  as running?.
  Entries are files under .swim/queue/: NNNN-q-<utc>-<hex>.queue while
  waiting, .running once a worker has claimed it (by renaming it, which
  is atomic: two workers can never claim the same entry), then
  done/NNNN-....yml with its result (run id, exit, each lane's result).
  .swim.log records queue, queue-run and queue-done events.
`,
	"schedule": `swim schedule LANES... TIME [--tz ZONE] [--approve FLAG]... [--note TEXT] [--parallel N] [--key PATH] [--yaml]
swim schedule LANES... TIME --daily [--until DATE | --for D] [--tz ZONE] [--approve FLAG]...
swim schedule list [--all] [--yaml]
swim schedule rm ID | pause ID | resume ID
swim queue add LANES... --at TIME [flags]     (the same, one-shot)

  Queue a batch that starts no earlier than TIME: swim queue add with a
  start time. The batch is pinned and approved now, exactly as a queued
  batch is (its lanes' jobs and manifests, --approve, signed with your
  key). No worker (swim watch) claims it before TIME; from then on it is
  an ordinary waiting batch, run as soon as its lanes are free. Late is
  late, not cancelled: with its lanes busy or no worker up at TIME, it
  starts when a worker next can (swim status and .swim.log say how late).
  To stop it before it starts: swim schedule rm ID.
  LANES are lane numbers or jobs; the last argument is always the time:
    13:30, 13:30:00, 1330, 133000   24-hour (no am/pm means 24-hour)
    1:30pm, 1:30 pm, 1pm, 12am      12-hour
    tomorrow 06:00, 2026-10-20 13:30
    +2h, +90m                       from now
  A bare time already past today means tomorrow (the reply says so); a
  dated time in the past, a time that doesn't happen (clocks going
  forward) and anything else ambiguous are refused, never guessed. Times
  are read in --tz, else schedule_tz (swim config), else this machine's
  zone; the start is kept in UTC. The reply:
    Lane 1 scheduled for 13:30:00(CDT) 18:30:00(UTC)
  then warnings: no swim watch running here, a batch ahead of it on its
  lanes, a lane it depends on holding a round that hasn't passed.
  -y is refused: approving every guard of a run hours away, with no one
  watching, is what -y suits least. Name each with --approve FLAG.
  Dependents wait with it: a batch queued after it is held while one of
  its lanes depends (After:, config deps:, at any depth) on a lane the
  scheduled batch holds, until that batch has run; then the usual After:
  check applies. swim run N of a scheduled lane is refused (it would run
  twice), and swim all leaves it to its schedule.
  The start time is signed with the batch: a worker refuses a signed
  batch whose file was edited (its time moved). The run's chain
  attestation records scheduled_for, and swim verify fails a run that
  started before it.
  list     scheduled batches by start time, held batches and what holds
           them (--all: consumed ones too; --yaml: swim.schedule-list/v1)
  rm ID    remove a scheduled batch that hasn't started; says which held
           batches it releases (they'll skip unless its lanes pass first)
  --yaml   the reply as swim.schedule/v1 (id, lanes, not_before, zone,
           warnings)
  --daily  a recurring schedule: the same pinned rounds every day at TIME
           (a time of day, wall clock in its zone, so 06:00 stays 06:00
           across DST; a day the clocks skip it runs at the first valid
           minute after). It's a template in .swim/queue/recurring/: each
           day a worker (swim watch) makes that day's occurrence, an
           ordinary scheduled batch (from: r-..., occurrence: the date),
           once, however many workers share the queue. Rules:
             it expires: --until DATE or --for D, 30 days by default, and
               never past lookup.yml's standing.max (as standing approvals);
               renew by scheduling it again
             pinned means pinned: an occurrence whose lane's script or
               manifest changed is skipped and the schedule paused, never
               run on the old approvals; resume works once they match
               again, else schedule it again to re-pin
             no catch-up, no stacking: after downtime only the latest due
               occurrence runs (late); earlier days are recorded as
               superseded, and a day due while the last one still runs is
               skipped as overlap
             swim stub and swim new on its lanes are refused (--force
               pauses it)
           Dependents belong in the same batch (ordered within each
           occurrence).
  pause ID, resume ID  stop and restart a recurring schedule's occurrences
`,
	"watch": `swim watch [--once] [-y] [--approved] [--interval D] [--batches N] [--no-tui]
swim watch --headless [--detach] [-y] [--approved] [--interval D]
swim watch --headless --status | --stop | --uninstall
swim watch --headless --install systemd|launchd [-y --i-mean-it]

  The queue's worker. Runs queued batches (swim queue add) side by side,
  up to --batches at once (watch_batches, default 4), each in a process
  of its own: a batch starts as soon as none of its lanes is in another
  batch or another swim run, oldest first, so a later batch whose lanes
  are free starts ahead of one that has to wait. Lanes that depend on
  (After:) or share locks with a lane of another batch wait for it as
  they do across swim runs. It runs each batch like swim run with the
  batch's lanes pinned and its approvals, never prompts, and consumes the
  entry when the run ends (passed, failed or interrupted alike: a half-run
  change is never retried on its own; swim queue requeue does that).
  Several workers can share a queue: claims are atomic, and a worker
  picks batches under a lock, so two never take the same lanes. While a
  batch runs, its process renews the claim's heartbeat every third of
  queue_lease (swim config). A batch this worker's swim can't run (the
  lock was upgraded after it started, or a lane's script is newer than it
  reads) is left queued for a worker that can, with why, in swim queue and
  .swim/workers/<host>-<pid>.yml, which says which swim the worker is. As
  it starts, the
  watch ends the round of any lane whose process died without ending it
  (as swim run does): END INTERRUPTED in its log, interrupted in status.
  --once         run every queued batch, then exit (non-zero if one failed)
  --approved     also queue, by itself, every lane whose exact script is fully
                 approved (swim approve, meeting lookup.yml's rules), once per
                 approval: the last approval is what makes it run
  -y, --yes      approve every guard in every batch, beyond what the batch
                 approves (the worker's say-so: logged as "approved by swim
                 watch -y (<user>)", and in the consumed entry as approved_by).
                 The batch's own --approve and -y keep their credit.
  --interval D   how often to check the queue and for idleness (default 2s)
  --batches N    run up to N batches at once (default watch_batches, 4);
                 --batches 1 runs one at a time, on the screen. A headless
                 worker runs one at a time.
  --no-tui       print what the worker waits for instead of the jobs screen
  On a terminal, while it waits, the watch shows every lane's job and the
  queue, with the batches running: ↑ ↓ (or k j) pick one, r runs it now,
  q queues it and enter follows its output (esc comes back).
    enter on a job follows its lane's log while it runs, in a batch or not;
                   v switches to its batch's output. When it ends, a header
                   bar says how: PASSED (green), FAILED (red, with the
                   failed steps) or INTERRUPTED, with the exit code and
                   time; enter on a batch does the same for the batch
    r on a job     runs it as swim run N does: you're at the terminal, so its
                   guards may ask
    q on a job     queues it as swim queue add N does (no guard approved: its
                   guards dry-run unless standing approvals cover them); the
                   worker takes it when its lanes are free
    d on a job     says what swim trash N would move; d again within 5s
                   trashes the lane (restorable: swim trash --restore STAMP)
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

  Headless: the same worker with no terminal at all, for a supervisor, the
  background or a service. It never shows the screen and never reads
  stdin. It needs this directory enrolled with a hub (swim hub enroll):
  with no terminal, the hub is where it's seen (its page marks it headless,
  and -y in bold). One per repo: .swim/headless.lock holds its pid, and a
  second --headless exits 2 naming it.
  --headless     run in the foreground (systemd, docker, tmux keep its output)
  --detach       start it in the background, in its own session: its pid goes
                 in .swim/headless.pid, its output in .swim/headless.log (rotated
                 at 10 MiB, 3 kept). Waits up to 5s for it to start.
  --status       is it running (exit 0) or not (exit 3, saying when a
                 pidfile is stale); which unit is installed
  --stop         SIGTERM to the worker holding the lock, once sure it is swim
                 watch --headless; waits for it to stop
  --install systemd|launchd
                 write a user unit for this repo and print how to enable it
                 (swim never runs that): ~/.config/systemd/user/swim-<id>.service,
                 or ~/Library/LaunchAgents/com.swim.<id>.plist logging to
                 ~/Library/Logs/swim/<id>.log. <id> is the repo directory's name
                 and a hash of its path. The unit pins swim's path, the repo,
                 PATH and SWIM_USER, restarts on failure, and allows term_grace
                 + 30s to stop. -y needs --i-mean-it: a standing approval of
                 every guard in a service that runs until removed.
  --uninstall    stop it, and remove this repo's unit files
  Signals: SIGTERM or SIGINT stop it after the batch in hand (the batch's
  lanes stop at the next step boundary, forced after term_grace; a second
  signal forces). SIGHUP reopens .swim/headless.log (for logrotate) and
  never touches a running lane. Told to stop, it exits 0, so a service
  manager doesn't restart it.
  Commands from the hub: a headless worker long-polls its hub for what
  hub users send (swim hub --help): interrupt a lane, drain (stop after the
  batch in hand, as SIGTERM), run a lane, or upload a run's log. The hub
  only relays; the worker checks each one and decides:
    new         each is taken once: the hub's epoch, the last seq and the
                ids of recent commands are kept in .swim/hub.yml, so a
                restarted or replaying hub can't make it act twice
    in time     past its expiry, or past headless.command_ttl (default
                10m; 2m for interrupt) after it was sent, it's acknowledged
                expired and not carried out
    allowed     swim.yml's headless.allow lists it (default: answer,
                interrupt, drain, log; run, trash, pause, unpause, queue,
                dequeue and requeue must be added)
    pinned      interrupt, pause and unpause name the run the page
                showed, run the script
                and manifest hashes, trash the lane's job (its script's
                hash when it has no Job: line): if the lane has
                moved on, it's refused as stale. A run is queued unsigned,
                by "hub:USER via URL", with no approvals of its own: its
                guards approve only as any unsigned batch's do. queue
                queues several lanes as one batch the same way, each
                pinned to its script and manifest; dequeue takes a batch
                off the queue (swim queue rm, never a running one);
                requeue queues a finished batch's lanes again as a new
                unsigned batch, without the old one's approvals or
                signature, refused when a script changed since. A trash
                (the Trash button, then its confirmation page) is swim
                trash N with every refusal, never --force.
    logs        a log is masked again with the secrets known now before it
                goes (only its last 8 MiB); headless.logs: off refuses them
  Every command, refused or not, is recorded in .swim.log and the chain
  ("hub <cmd> by <user> via <url>: <outcome>"), and acknowledged to the
  hub. swim.yml (only; config.yml can't set it):
    headless:
      allow: [answer, interrupt, drain, log]   # add run, trash, pause, unpause, queue, dequeue, requeue to allow them
      logs: on                                 # off: logs stay here
      command_ttl: 10m
      approvals: signed      # signed | hub: how guards are answered
      answer_timeout: 30m    # no answer by then: a decline
    hub:
      logs: request          # request (a log when asked) | stream (also
                             # each round's log as its steps end, and
                             # finished runs' step output) | off (none;
                             # headless.logs: off is off too)
  Guards through the hub: a guard that nothing else approves (no signed
  approval, no flag the batch approves, no -y) is shown in the hub's
  Approvals inbox as a question, and the lane waits for an answer up to
  headless.answer_timeout (or its round's Timeout). An approver answers:
    signed      swim approve --hub WORKER/QUESTION (or Answer in the inbox:
                ssh-keygen -Y sign the statement shown, paste your key and
                the signature). The statement names the question, this worker,
                the lane, job, run, script, manifest, evidence, guard,
                approve or decline, and when it expires; the worker builds
                it from its own facts and checks the signature against
                lookup.yml (as the run started). Signers count until
                lookup.yml's rule for the guard is met. Recorded as
                hub-signed, with each signature (swim verify checks them).
    button      only with headless.approvals: hub, by a hub operator whom
                lookup.yml lists, and never for a guard a require: rule
                names or that needs evidence: those wait for signatures.
                Recorded as hub-button (unsigned).
  Decline is a button, or signed. No answer by the deadline, the hub
  unreachable, or the worker stopping all decline: the guard is a dry
  run. Without lookup.yml nobody can answer. A worker that doesn't allow
  answer never asks: its guards dry-run as in any batch.
`,
	"modules": `swim modules [ls] [--yaml]
swim modules show NAME|./DIR
swim modules new NAME [--kind bash|go] [--dir DIR]
swim modules check [--yaml]
swim modules lock
swim modules get [--upgrade [NAME|ADDRESS]]

  The modules this repo's lanes can declare (# Modules:) and call with mod
  and mod_env: the built-in ones, and the repo's in swim.yml's modules: dir.
  See MODULES in swim --help.
  ls       each module: version, kind, where it comes from, what it does,
           its declared effects, and whether swim.yml allows it.
           --yaml: swim.modules/v1
  show     a module's module.yml and its source (a built-in Go module's is
           compiled into swim), for reading before approving a lane that
           uses it
  new      write a repo module's skeleton: DIR/NAME/ (default swim.yml's
           modules: dir, else modules) with a module.yml and NAME.sh, or
           main.go and go.mod with --kind go; it says what to add to
           swim.yml to allow it
  check    for each module the lanes declare: the lanes, the version they
           ask for (tf@1.2), what .swim.lock pins and what this swim has,
           and the fix for a mismatch. Exit 1 on one, or on a lane whose
           modules don't resolve. --yaml: swim.modules-check/v1
  lock     pin in .swim.lock the built-in modules the lanes use, as this
           swim has them (version and hash), and say what changed. A swim
           whose built-in differs then refuses those lanes. swim lock
           --upgrade does it too. Commit the lock; lanes using a changed
           module need approving again.
  get      fetch the modules the lanes name by source address
           (github.com/org/swim-modules//pager@2) into .swim/modules/, and
           pin each in .swim.lock: the newest tag the lanes accept, its
           commit and files' hash. A pinned version is kept (and fetched
           again if its copy is gone or changed); --upgrade takes the newest
           accepted tag, for all or the one named. swim.yml must allow the
           address. See MODULES in swim --help.
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
  that preset. See CACHE in swim --help.
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
	"secret": `swim secret set NAME [--all|--repo]
swim secret get NAME
swim secret ls [--yaml]
swim secret rm NAME [--all|--repo]
swim secret grant WORKER NAME... [--guard FLAG[,FLAG]] [--hub URL] [--admin-token T]
swim secret revoke WORKER NAME... [--hub URL] [--admin-token T]
swim secret grants [--hub URL] [--admin-token T]

  Secrets encrypted to your SSH key with age (https://age-encryption.org),
  kept in secrets.yml next to swim's config (0600). Names are readable;
  each value is encrypted on its own. Two scopes: --all (every repo) and
  --repo (this git repo, the default inside one); a repo's own secret
  wins over an --all one of the same name.
  set    reads the value from a hidden prompt, or from stdin
         (echo "$X" | swim secret set NAME), never the command line, so it
         stays out of shell history and ps
  get    prints the value: inside a swim run from the run's secrets broker,
         outside one by decrypting with your key (asking its passphrase
         on the terminal if it has one). A guarded secret (below) inside a
         run only once its guard is approved; outside one at your own shell
         (recorded in .swim.log), never in a lane run with bash
  ls     names, scopes and when each was set, and the guard swim.yml puts
         on it; never values
  rm     removes one from a scope
  The key is secrets_key, else signing_key, else ~/.ssh/id_ed25519
  (ssh-ed25519 or ssh-rsa). Encrypting needs only its .pub; decrypting
  needs the private key on disk: ssh-agent can't decrypt.
  SWIM_SECRET_<NAME> in the environment stands in for a secret (CI, where
  there is no key) and wins over the store.
  Names: a letter, then letters, digits and _. A repo-scoped set or rm is
  recorded in .swim.log by name, never value.
  In lanes:
    bash      DB=$(swim secret get db_password) || stop "no db password"
              (lanes don't run with set -e: check the get)
    Python    password = swim.secret.get("db_password")
    TS / JS   const password = await secret.get("db_password")
    Go        password, err := swim.Secret.Get("db_password")
  A swim run decrypts the repo's secrets once, at start (a protected key's
  passphrase is asked then, only if a lane looks like it uses secrets;
  swim watch and CI can't ask), and serves them from a broker on a
  private socket. Each lane process says which lane it is with its own
  token (SWIM_APPROVAL_TOKEN), checked against the process that asks, and
  masks, as ***(name), every secret its lane may get.
  .swim.log records each lane's use: "secret  swim 2  db_password (repo
  scope, get)". swim lint warns about names the store doesn't have.
  Guarded secrets: swim.yml can guard a secret by guard flags,
      secrets:
        PROD_DB_ADMIN_URL: { guard: ORDERS_ALLOW_DROP }
        AWS_PROD_WRITE_ROLE: { guard: [INFRA_APPLY, INFRA_DESTROY], release: step }
  and the broker releases it to a lane only once one of them was approved
  for that lane (that process) in this run, as the launcher saw it: the
  prompt, -y, a signed approval, a queued batch's --approve, or FLAG=1 in
  swim run's own environment. FLAG=1 a lane sets itself never counts.
  Otherwise the get fails: "refused: NAME is guarded by FLAG, which isn't
  approved in this run" (a WARN in the lane's log, .swim.log and a
  secret-refused chain entry). A release is a secret-released entry: the
  name, the flag, how it was approved and the approve entry, never the
  value. release: step hands it out once; swim plan --check never does.
  SWIM_SECRET_<NAME> of a guarded secret never reaches a lane's
  environment. grant --guard puts a guard on a hub grant (both it and
  swim.yml's must be approved).
  From a hub (swim hub --help): grant encrypts NAME, as this repo sees it,
  to an approved worker's own key and gives the hub only the ciphertext;
  the hub never holds a value or a key to decrypt one. The worker's runs
  (swim run, swim watch) fetch their grants at start, decrypt them with its
  key and resolve them under its own store, as scope "hub"; swim secret get
  there does too. revoke deletes a grant (its next run can't get it: rotate
  the secret at its source if it mattered); grants lists them, by name.
  The hub's audit.log records approvals, grants and each fetch. A worker
  that enrolls with a new key drops back to pending and loses its grants.
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
swim approve N|JOB --guards A --cell C              one cell of a matrix round only
swim approve N|JOB [--guards A,B | --all] [--expires D] [--note T] [--key K] [--no-evidence]
swim approve --auto USER [--guards A,B | --all] [--expires D] [--key K]
swim approve --hub WORKER/QUESTION [--decline] [--key K] [--url URL] [--token T]

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
  With evidence    a round with evidence steps (evidence "tf plan" ...), or a
                   guard whose lookup.yml rule says evidence: true, is
                   approved with its latest evidence (from swim plan --check N
                   or a run): swim approve shows it and signs its hash too
                   (approvals/lane.N.<sha12>.m<manifest8>.e<evidence8>.signature). It
                   refuses with no evidence, or evidence for another script
                   version. At run time the approval counts only while the
                   run's evidence matches; when the plan changed, the log
                   shows a WARN and a diff, and the guard asks (or dry-runs).
                   swim watch doesn't compare evidence (and says so).
                   --no-evidence approves the script only.
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
  --hub WORKER/QUESTION
             answer a guard waiting on a headless worker (swim watch
             --help): fetch the question from the hub (WORKER by name or
             id; QUESTION its id, or 6+ leading characters, both in the
             hub's Approvals inbox), show it, sign its statement with your key and
             send it as your hub user. --decline signs a decline. The hub
             is --url, else $SWIM_HUB_URL, swim hub login's, or this
             repo's enrollment; your hub user token is --token, else
             $SWIM_HUB_USER_TOKEN. The worker checks the signature against
             its own lookup.yml; the hub only relays it.
`,
	"revoke": `swim revoke N|JOB [--key K]
swim revoke --auto USER [--key K]

  Withdraw your approval: of lane N's script (every version you approved),
  or your standing approval of USER's queued jobs. Adds a signed revoke
  event; your approval stops counting from then on (approving again adds
  another approve event). A round that already ran keeps its record.
`,
	"verify": `swim verify [--strict] [--trust-pin SHA256] [--against DEST] [--yaml]

  Prove the chain of custody (.swim/chain.jsonl), in levels. Each line
  says whether the level holds (yes, no, partial, n/a) and what it rests on:
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
    anchored    the chain still holds the entry the newest checkpoint at
                each anchor (chain_anchors) recorded: entries removed from
                its end, or a chain replaced wholesale, after that
                checkpoint left the machine, fail it. A checkpoint with a
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
  Exits 1 when a level says no or anything is BROKEN; --strict also on n/a,
  partial and any warning.
  --trust-pin SHA256   check trust.yml against this hash instead of the pin
                       in ~/.config/swim/trust-pins.yml (CI, which keeps no
                       pins; swim keys pin prints it). Also SWIM_TRUST_PIN.
  --against DEST       anchor to DEST (file:/path, hub) instead of
                       chain_anchors: its newest checkpoint, and the entries
                       swim chain export copied there must be the chain's
                       first ones (so a lost or wiped repo can be checked).
  --yaml               the result as data (schema swim.verify/v1): each
                       level a field of its own (consistent: "yes"), so CI
                       can require any of them; ok is what the exit code says.
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
    hub          the hub this directory is enrolled with (swim hub enroll):
                 it keeps each worker's checkpoints, refuses one whose seq
                 doesn't go forward, and keeps them after the worker leaves
                 (with --state). Checkpoints only: export needs file:.
  An anchor proves nothing about entries written after its last checkpoint,
  and someone holding the runner key and write access to every anchor can
  rewrite everything: keep anchors out of the operator's reach.
`,
	"keys": `swim keys
swim keys trust [KEY | FILE.pub] [--remove] [--sign] [--key K]
swim keys pin

  Who's who: lookup.yml's users with their key fingerprints, groups (with
  nested groups expanded), require rules, trust.yml and its pin, and the key
  this machine signs with and whom it maps to.
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
	"hub": `swim hub [--addr HOST:PORT] [--token T] [--state DIR] [--admin-token T] [--no-tui]
         [--tls-cert FILE --tls-key FILE] [--insecure] [--public-url URL] [--private]
         [--retain AGE] [--retain-bytes SIZE] [--trust-tailscale-identity]
         [--admin-tailscale LOGIN[,LOGIN]]
swim hub enroll URL [--token T] [--name N] [--namespace NS] [--ca FILE] [--insecure]
swim hub leave | status
swim hub login URL --admin-token T [--ca FILE] [--insecure]
swim hub admit | reject [WORKER] [--hub URL] [--admin-token T]
swim hub approve | unapprove WORKER [--hub URL] [--admin-token T]
swim hub user add NAME --role viewer|operator|admin [--tailscale LOGIN] | remove NAME | list
         [--hub URL] [--admin-token T]

  A watch hub: one page showing what many swim workers are doing.
  swim hub         serve it (default 127.0.0.1:7700): an app at / of every
                   enrolled worker (switch with Ctrl-K), its runs, a run's
                   lanes as a graph, a job's stages, steps and output, its
                   lanes now (the live tail of running ones), its queue, and
                   every guard waiting for an answer, updating as reports
                   arrive, with the controls below for a logged-in user; and
                   the read API below for other services. On a terminal it
                   shows a live screen: every
                   worker (state, activity, a mark per lane) with the
                   selected one's running lanes and queue below; Enter opens
                   a worker (each lane, the selected lane's message and log
                   tail, its queue and recent events), Esc comes back,
                   Ctrl-C stops the hub; a approves the selected worker for
                   secrets, u withdraws it. --no-tui (or not a terminal)
                   prints the URL and enroll command instead. Viewing the
                   pages needs no login, so keep the default localhost bind
                   or pass --private; sending commands and reading full
                   logs needs a hub user either way.
  --private        viewing needs a login too: the pages redirect to /login
                   and the read API answers 401 without a hub user's
                   session or token (or, for the list, the admin token).
  Read API (JSON, schema swim.hub/v2):
    GET /api/health                     no login, no data: the hub answers
    GET /api/enrollments                the namespace tabs and every worker
                                        (?ns=NS: one namespace's)
    GET /api/enrollments/ID             one worker's latest snapshot
    GET /api/enrollments/ID/runs        its kept runs, newest first
                                        (?limit=N&before=RUN; next: the page after)
    GET /api/enrollments/ID/runs/RUN    a run's record, result and jobs
    GET .../runs/RUN/jobs/LANE          a lane's round: stages, steps, output (a user)
    GET .../runs/RUN/jobs/LANE/log      its log as text, once on the hub (a user)
    GET /api/events                     server-sent events: snapshot, run and
                                        log, as they happen (?enrollment=ID);
                                        nothing is replayed, so a client reads
                                        the state when it (re)connects
  TLS: --tls-cert FILE --tls-key FILE (PEM) serve https (TLS 1.2 or later);
                   the certificate reloads when either file changes, so a
                   renewal needs no restart. swim makes no certificates.
                   Without TLS the hub serves plain HTTP only on a loopback
                   address: an --addr on every interface (0.0.0.0, ::, :PORT)
                   or another host is refused (exit 2) before it binds,
                   unless --insecure. Behind a TLS proxy, keep the hub on
                   127.0.0.1 and point the proxy at it.
  --public-url URL the address clients reach the hub at through a proxy on
                   this host (https://swim.example.ts.net): the enroll and
                   login hints print it, an https URL makes session cookies
                   Secure, and the audit log names the client from the
                   proxy's X-Forwarded-For (trusted only from a loopback
                   peer; the last address, the one the proxy added).
  --trust-tailscale-identity
                   sign hub users in by tailscale serve's Tailscale-User-Login
                   header: a user given that login (user add --tailscale)
                   gets a session on loading the app or /login, as their
                   token would. Only on a loopback --addr, and only from a
                   loopback peer. The header authorizes nothing itself
                   (commands still take the session and its CSRF token), and
                   such a session can't answer a guard with a button.
  --admin-tailscale LOGIN[,LOGIN]
                   with --trust-tailscale-identity: these logins sign in as
                   admin users. The first sign-in by one adds them (named
                   after the login, zgates for zgates@example.com, with no
                   token anyone holds) and a later one makes them admin
                   again if their role changed, so the hub's command line
                   says who runs it. Nobody becomes admin just by being
                   first: a login not named here has to be added.
                   --state DIR (default ~/.config/swim/hub) keeps the
                   workers, their keys, approvals and grants, and the admin
                   token (DIR/admin-token, made on first start; --admin-token
                   or $SWIM_HUB_ADMIN_TOKEN overrides), plus DIR/audit.log.
  Admission: any swim may enroll, with no token; it then waits until the
  hub's admin admits it (the app's Admin page, or swim hub admit WORKER;
  swim hub admit alone lists who waits, with each one's host, directory,
  namespace, key fingerprint and the address it enrolled from; swim hub
  reject WORKER turns one away, and it may enroll and wait again). While
  it waits it shows nowhere else: its reports, runs, logs, checkpoints and
  grants are refused and it gets no commands. At most 50 wait at once. A
  directory enrolled already is enrolled again only with its own token
  (its .swim/hub.yml), so nobody takes an admitted worker's place; an
  admin's enrollment (the admin token) admits it at once. Workers enrolled
  before admission stay admitted.
  Tokens: the enroll token (--token, else $SWIM_HUB_TOKEN, else a new one
  each start) no longer admits anything (workers from before admission
  still send it); each worker gets its own token when it enrolls,
  for its reports and secrets, so a hub restart doesn't strand it; the
  admin token approves workers, manages grants and manages users, and
  sends no commands; each hub user's token sends commands as their role
  allows, and an admin user's also does what the admin token does (swim
  hub login URL --admin-token THEIR-TOKEN for the CLI's admin commands).
  swim hub user add NAME --role viewer|operator|admin
                   add a hub user (admin) and print their token, once: the
                   hub keeps only its hash. Use the name lookup.yml knows
                   them by. They log in at URL/login (a 12-hour session,
                   kept hashed in --state, so a restart keeps it; its
                   cookie: HttpOnly, SameSite=Strict, Secure under TLS;
                   every form carries a per-session CSRF token) or send the
                   token as Authorization: Bearer. Adding a name again gives
                   a new token and role and ends their sessions. remove
                   ends them too; list shows every user and role.
  Commands: on a headless worker's Lanes page (swim watch --headless) a
  user logged in gets controls: Pause and Unpause, Interrupt a running
  lane (pinned to the run shown), Stop the worker after this batch
  (drain), Run now an idle lane (pinned to the script and manifest shown;
  queued unsigned, by hub:USER via URL, with no approvals of its own),
  Trash, Fetch log (the run's full log, uploaded masked by the worker and
  kept in the hub's memory only: 8 MiB a log, 64 MiB in all, 15 minutes),
  and in the Approvals inbox, for a guard waiting for an answer: a signed
  answer (the statement to sign, for ssh-keygen -Y sign or swim approve
  --hub), Approve (a button, where the worker's swim.yml says
  headless.approvals: hub) and Decline. viewer may
  ask for logs and send signed answers (the signature is what counts);
  operator may do all of it; admin is an operator who also manages users,
  approves workers' keys and revokes grants on the app's Admin page (a
  grant is made with swim secret grant, which encrypts the value to the
  worker's key on the admin's machine, so the hub never sees it). The hub keeps commands in --state DIR
  and only relays them: the worker long-polls for them with its own
  token, checks each against swim.yml's headless: block, its expiry
  (10 minutes, interrupt 2) and its pin, and acknowledges done, refused
  or expired, which the Commands panel shows. See swim watch --help.
  swim hub enroll URL [--name N] [--namespace NS]
                   enroll this directory, to wait until the hub's admin
                   admits it (see Admission; swim hub status says whether it
                   waits), as name (the directory's) under a
                   namespace, the dashboard tab it shows in: its repo, from
                   the origin remote ("github.com/org/api" for
                   git@github.com:org/api.git), so clones of one repo on
                   different machines share a tab; the directory's name
                   with no remote. --namespace NS names it instead. It
                   groups the pages and grants nothing. Enroll saves
                   .swim/hub.yml (private), and from then on swim watch here
                   reports to the hub every --interval, also during batches.
                   It makes this directory's own age key for secrets
                   (~/.config/swim/worker-keys/, no passphrase, so swim
                   watch decrypts unattended) and prints its fingerprint:
                   the worker shows as PENDING until approved.
                   --ca FILE trusts that certificate (the hub's own, if
                   self-signed, or its CA) for an https:// hub instead of
                   the system's roots, and is saved in .swim/hub.yml. An
                   http:// URL to another machine is refused unless
                   --insecure (http://127.0.0.1 or localhost is fine);
                   swim doctor warns about such an enrollment (hub-http).
  swim hub login URL --admin-token T [--ca FILE]
                   save the hub's URL and admin token (~/.config/swim/
                   hub-admin.yml, private; --ca and --insecure as for
                   enroll) for the commands below; $SWIM_HUB_CA (a file)
                   overrides the saved CA
  swim hub approve WORKER
                   approve a worker (by name or id) for secrets: compare the
                   key fingerprint with what its enroll printed. unapprove
                   withdraws it and deletes its grants.
  swim hub leave   stop reporting (and drop off the hub)
  swim hub status  this directory's enrollment, and whether the hub answers
  Workers push their reports, so they can sit behind NAT or a firewall; the
  hub keeps only each worker's latest report in memory (a restarted hub
  fills back up as workers report). A worker silent for 3 intervals shows
  as stale. Lane output in reports is the logs' text, secrets masked.
  Runs: swim watch also sends each finished run (its newest 20 the hub
  hasn't taken): the run record (lanes, After, timings, results) and every
  lane's round in it (stages, steps, commands, exits, durations), with the
  steps' output only where the repo's swim.yml says hub.logs: stream. The
  hub keeps them in --state DIR/runs/, through restarts, for --retain AGE
  (default 30d) and at most --retain-bytes SIZE (default 2GB; the oldest
  go first); 0 for no limit. .swim/hub-runs.yml remembers what the hub
  took; a new enrollment (a hub that lost its state) gets them again.
  Under hub.logs: stream, swim watch also sends each lane's round as its
  log grows (a step's block once the step ends; the running step is in
  the report's tail), masked, kept with its run (DIR/runs/), and read at
  .../runs/RUN/jobs/LANE/log while the lane runs and after.
  Secrets: swim secret grant WORKER NAME encrypts a secret on your machine
  to that worker's key; the hub keeps only the ciphertext, and the worker's
  runs fetch and decrypt it at start (see swim secret --help).
`,
	"interrupt": `swim interrupt N|JOB

  Ask one running lane to stop at its next step boundary, from any
  terminal: its current step finishes, no further step starts, and the round
  ends interrupted (STOP  interrupted by operator (after: <step>), exit 130).
  Other lanes keep running. The same thing the first Ctrl-C does to every
  lane of a swim run. A paused lane (swim pause) ends the same way, its
  outcome handlers run.
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
  when the lease runs out. swim status shows it; swim plan warns about it.
  swim claim N takes or refreshes it explicitly (--for D); --release gives
  it up (--force: another session's); --break takes over a live claim with
  a reason, and --force on new, stub or archive does too: both are logged
  as claim-broken. A session is SWIM_SESSION, else user@host: set
  SWIM_SESSION per session, or two sessions of one user on one machine are
  one. Claims are advisory: they guard swim's commands, not an editor
  writing lane.N.sh, and stay in this checkout's .swim/claims/.
`,
	"pause": `swim pause N|JOB ... [--wait] [--note TEXT]
swim pause --all [--wait] [--note TEXT]

  Hold a running lane before its next step, without ending it: its current
  step finishes, then the round waits, its process, staged script,
  approvals, slot and locks all kept. The log gets
  PAUSE  by <who> after "<step>" (next: "<step>"); swim status shows it
  paused. A lane still waiting in a run is held before it starts. It holds
  before a run, gate, snapshot, evidence, guard or confirm, and an SDK
  lane before its next step; never inside a step, and never in an outcome
  handler. A pause doesn't expire. The round's Timeout: doesn't count the
  paused time. --wait returns once the lane holds; --note says why.
  --all holds every lane in the repo and stops queue workers claiming
  batches (a scheduled batch due meanwhile waits), until swim unpause --all.
  Ways out: swim unpause N (or swim resume N) goes on at the step it held
  before (RESUME  by <who> (paused <time>)); swim interrupt N, or the first
  Ctrl-C of its run, ends it interrupted; swim run N on a new job ends it
  (asked at a terminal, or --replace) and runs the new one.
`,
	"unpause": `swim unpause N|JOB ... | --all
swim resume  N|JOB ... | --all      (the same command)

  Let paused lanes go on at the step they held before (swim pause). --all
  lifts every lane's pause and the repo-wide one, so workers claim batches
  again. The record says unpause whichever name is typed. A lane that
  isn't paused is left alone (exit 0).
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
    secret-guard (warn)    a guarded secret (swim.yml secrets:) got with no
                           guard of its flag before it: swim run refuses it
    secret-argv (warn)     $(swim secret get NAME) inline in a command's
                           arguments, readable with ps: assign it first and
                           pass it in an environment variable or on stdin
    ambient-credential (warn) a credential swim.yml lists in
                           ambient_credentials (AWS_PROFILE=, --profile) used
                           directly: no guard stands in its way
    header (warn)          a Timeout, Step-Timeout, Locks, Always or
                           Rerun-Children value swim ignores
    env (error)            the lane's env files don't parse, or set its guard flag
    handler (error)        handlers or Fail: without syntax 3; a near-miss handler
                           name (on_fail, onfailure, ...) swim would never call
    handler-reached (warn) an on_failure with a guard that never checks reached
    module (error)         a # Modules: entry that doesn't resolve or swim.yml
                           doesn't allow, a module command the script doesn't
                           declare, or modules without syntax 4
    module-effects (warn)  a module used against its declared effects:
                           credentials without mod_env, not read-only before
                           stage change, destructive outside a guard
    rerun-guard (warn)     a guarded round downstream of a Rerun-Children lane
                           (swim all reruns it even after it passed)
    group (error)          a lane's jobs: none with steps, one defined twice, one
                           of another lane, a header key on the wrong side
                           (Always: on the lane, After: on a job), or a group
                           file (lanes.<name>.sh) for a lane another file has
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
    sdk-bypass (info)      an SDK lane runs commands with subprocess or
                           child_process instead of r.run
    check-unsafe (warn)    a destructive-looking command (terraform apply, a
                           delete) before stage change: plan --check runs it
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
    tracked (warn)            git tracks lane scripts, .lane*.rc or .swim/
    stale-pid (warn)          .swim/laneN.pid naming a process that is gone
    toolchain (error)         config toolchain fails in bash -c (30s limit)
    git (error)               git missing, or not inside a git repo
    header-env-secret (warn)  header_env lists a secret variable
    lock (info/warn)          .swim.lock missing or in the old form (info),
                              invalid or pinning other syntaxes (warn)
    env-file (error)          an env file that doesn't parse, or sets a
                              lane's guard flag
    lane-rc (warn)            a deprecated .lane.N.rc
    outputs-old (info)        round outputs older than outputs_keep (default
                              14d) that no lane's latest round uses; --fix
                              removes them
    hub-http (warn)           .swim/hub.yml enrolls with a hub over plain
                              http on another machine
    anchors-none (info)       the repo has a chain but no chain_anchors
    checkpoint-unsent (warn)  a chain anchor hasn't had the latest checkpoint
    trust-unpinned (warn/error) trust.yml not pinned on this machine yet
                              (warn), or changed since the pin with no
                              signed change (error): swim keys --help
    stages-old (info)         runs' staged lane copies (.swim/runs/<run>/)
                              older than outputs_keep, of no lane's latest
                              round; --fix removes them (run records stay)
    standing-approval (warn)  a standing approval still counting under the
                              old terms: it never expires (until lookup.yml
                              sets standing.max), or a lookup.yml rule
                              change doesn't stale it
    headless-service (info)   a headless worker runs here, or a service
                              file for one is installed
    headless-stale (warn)     .swim/headless.pid left by a headless worker
                              that is gone (killed?)
    hub-unreachable (warn)    a headless worker's hub can't be reached:
                              guards waiting on it decline (5s limit)
    headless-hub-answers (warn) swim.yml's headless.approvals is hub: a hub
                              user's click approves guards no require:
                              rule names
    headless-yes (warn)       an installed headless service runs with -y
    prune-reclaim (info)      swim prune --older 30d would free over 100 MB
    cache-dir (error)         the cache directory is a link, isn't yours, or
                              others can write it: lanes with # Cache: won't
                              start
    cache-unused (info)       tool caches unused for 30 days, or the repo's
                              caches over cache_max
    remote-running (warn)     a lane has run on another machine sharing the
                              repo for over a day; if that machine is gone,
                              release it (rm .swim/laneN.pid, then swim
                              status --rebuild)
    pause-long (warn)         a lane paused (swim pause) longer than
                              pause_warn (4h), holding its locks and slot;
                              or the repo-wide pause left on that long
    schedule-no-worker (warn) batches are scheduled, but no swim watch runs
                              here to start them
    guarded-secrets (info/warn) lists swim.yml's guarded secrets (info);
                              warns when the secrets key has no passphrase:
                              a lane could decrypt them itself
    ambient-credential (warn) a profile swim.yml lists in
                              ambient_credentials is configured in ~/.aws:
                              every lane can use it without a guard

  --fix applies only safe local repairs, then checks again: it rewrites
  swim's .gitignore block, removes stale pid files, and moves a .lane.N.rc of
  plain KEY=VALUE lines into .env.swim [lane N] (the original is kept under
  .swim/migrations/; one that sets a guard flag is refused). It never edits
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
  new ones (swim plan / status / log still work, with a warning):
    - lock is newer than this swim: update swim, then run again:
        go install github.com/gates-brightly/swimlane/cmd/swim@latest
        (or in a swimlane checkout: git pull && just install)
    - lock is older than this swim: the repo was set up before a syntax
      changed. Read swim changelog for what changed, update the lane scripts
      (swim new writes the current template), let running lanes finish, then
      record that the repo is ready with: swim lock --upgrade
      (it warns about a worker up now that reads other syntaxes: that
      worker leaves batches queued until it's updated and restarted)

  swim lock             show this swim's version and syntaxes, and the repo's lock
  swim lock --upgrade   move the lock up to this swim's syntaxes (and rewrite
                        an old-form lock)

  Syntax versions:
    script 4, log 2, state 2   modules: a lane script that uses # Modules:,
                               mod or mod_env declares # swim: syntax 4,
                               so a swim without modules refuses it
                               instead of running it without them
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

  Print the full guide, or one command's detail.
`,
}

// For is a command's help: Commands, with an alias (swim resume is swim
// unpause) resolved.
func For(name string) (string, bool) {
	if name == "resume" {
		name = "unpause"
	}
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
// FullOrder is the order of the command details in swim --help; every
// command in Commands is in it (help_test checks).
var FullOrder = []string{"init", "config", "env", "secret", "cache", "modules", "new", "plan", "run", "all", "status", "log", "report", "timeline", "observe", "step", "lib",
	"gate", "snapshot", "mod", "guard", "stage", "drift", "stop", "msg", "input", "output",
	"archive", "clean", "prune", "trash", "stub", "note", "lock", "changelog", "version", "skill", "interrupt", "pause", "unpause", "claim", "queue", "schedule", "watch", "hub", "wait", "approve", "revoke",
	"verify", "history", "chain", "keys", "ci", "lint", "doctor"}

func Full() string {
	s := Guide + "\nCOMMAND DETAIL\n"
	for _, order := range FullOrder {
		s += "\n" + Commands[order]
	}
	return s
}
