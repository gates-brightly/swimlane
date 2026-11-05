// Package help holds swim's user and agent documentation: the short
// overview `swim --help` prints, per-command detail, and the reference
// (docs/reference, embedded) that swim guide prints.
package help

import (
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane"
)

// Index is docs/reference/README.md: what swim guide prints with no topic.
var Index = page("README")

// Topics are the reference's pages, in the index's order.
var Topics = func() []string {
	var names []string
	for _, m := range regexp.MustCompile(`\]\(([a-z-]+)\.md\)`).FindAllStringSubmatch(Index, -1) {
		names = append(names, m[1])
	}
	return names
}()

// Topic is one reference page.
func Topic(name string) (string, bool) {
	for _, t := range Topics {
		if t == name {
			return page(name), true
		}
	}
	return "", false
}

func page(name string) string {
	data, err := swimlane.Reference.ReadFile("docs/reference/" + name + ".md")
	if err != nil {
		panic(err)
	}
	return string(data)
}

// Commands maps each command to its detailed help.
var Commands = map[string]string{
	"init": `swim init

  Set up the current repo (run from anywhere inside it):
    - creates ~/.config/swim/config.yml if missing (an existing one is left
      as it is; init writes nothing for the repo there) and says the
      repo's name, the repos: key its settings go under
    - adds a marked block to .gitignore: lane.[0-9]*, .env.swim, .swim/,
      .swim.log
    - creates .swim/status.yml with every lane empty
    - records "init" in .swim.log, the project log (git-ignored too; drop it
      from the block if you want to commit the project's history)
  Safe to run again.
`,
	"config": `swim config [edit | KEY [VALUE]] [--repo | --branch] [--path]

  Config is ~/.config/swim/config.yml, in layers, later winning key by key:
  swim's default < defaults: < repos.NAME < repos.NAME/BRANCH. NAME is the
  repo's name: its origin remote URL's last part without .git (swimlane,
  from git@github.com:gates-brightly/swimlane.git), else its toplevel
  directory's name; every worktree of a repo shares it. BRANCH is the
  checked-out branch, matched exactly (none on a detached HEAD).
  With no key on a terminal (or with edit), every setting on one screen to
  change in place: its value, the layer it comes from, its default and
  what it takes. Enter, Space or ← → step a key with choices; Enter on
  another types its value on the bottom line (Esc cancels), checked before
  it's written; d removes it from this section; q quits. The screen edits
  defaults:, or with --repo this repo's section and with --branch this
  branch's. Only what differs from what the section inherits is written,
  and the first write backs the file up as config.yml-N.bak.
  With no key otherwise, print the effective configuration for this repo,
  each value with the layer it comes from (default, defaults, repo,
  repo/branch), or with --path just the config file location. The output
  ends with the effective blocked_commands: every layer's, added up (swim
  blocks none by itself).
  swim config KEY          print KEY's effective value
  swim config KEY VALUE    set KEY under defaults:, keeping the file's comments;
                           --repo: in repos.NAME; --branch: in repos.NAME/BRANCH
  Keys:
    queue_lease  duration (10s or more)   a queue watcher renews its claim's
                 heartbeat every third of this while it runs a batch; seen
                 from another host, a claim with an older heartbeat is
                 orphaned. Default 2m.
    claim_lease  duration (1m or more)    how long the claim swim new takes
                 on a lane lasts unless refreshed. Default 2h.
    watch_batches  a number (1 or more)   how many queued batches one swim
                 watch runs at once (default 4; swim watch --batches N).
    notify       true or false            terminal notifications and a
                 progress bar on the tab (OSC 777 and OSC 9;4, as Ghostty
                 shows them): when a lane waits on you and when a run or a
                 batch ends. Each title starts with the repo, and the branch
                 off the default: swimlane@fix · lane 2 needs you. Only on
                 a terminal, never in CI. Default false.
    notify_url   an http(s) URL           send the same notifications there,
                 terminal or not, as ntfy messages (title, priority, tags;
                 with curl): e.g. https://ntfy.sh/TOPIC.
    notify_command  a shell command       run instead (sh -c, in the repo
                 root), with SWIM_EVENT (ask, end, test), SWIM_RESULT
                 (passed, failed, interrupted, skipped, error), SWIM_TITLE,
                 SWIM_BODY, SWIM_LANES, SWIM_BATCH, SWIM_REPO,
                 SWIM_BRANCH, SWIM_ROOT, SWIM_PRIORITY (high, default),
                 SWIM_TAGS and SWIM_NOTIFY_URL set.
                 Each send is recorded in ~/.local/state/swim/notify.json:
                 swim config shows the last (t on its screen sends a test),
                 swim doctor warns when it failed, and a failure is a warn
                 line in .swim.log.
    runners.EXT  a command                the runner for lane.N.EXT scripts with
                 no #! line, e.g. swim config runners.ts tsx. Written to the
                 repo's section (or the branch's, with --branch).
  Unknown keys and bad values are rejected.
  There is no lane count to set: swim shows the lanes in use (a lane script
  that isn't a stub, a current log, or a running lane). swim new N starts
  one (1..99); swim archive N drops one. A lanes: setting is ignored.
  Config lives outside the repo, in ~/.config/swim/config.yml
  ($XDG_CONFIG_HOME/swim/config.yml if set). Watchers must not edit it; give
  the operator exact YAML lines instead.
`,
	"new": `swim new N|TASK "<round goal>" [--round-id ID] [--force]

  swim new TASK (a name: renew-certs) writes the lowest lane not in use
  and names it with a # Task: line, so swim run TASK, log TASK, status
  and queue find it by name. The lane number stays what the chain
  records; the name is an alias.
  A name is a-z, 0-9 and - (a letter first, at most 40, not only hex
  digits and -), not a swim command, and one lane's alone (swim lint).
  Where swim takes a lane it reads a number as a lane, then a name as a
  task, then a round id.
  Write lane.N.sh from the template: the goal on its Round: line, a new
  round id on its Round-ID: line, the toolchain preamble from config, and
  example snapshot/gate/guard/verify steps to replace. Prints the round id.
  Marks the lane ready in status.yml, with its round and round id.
  Refuses if lane N is running, or already holds a ready round (use
  --force to replace a ready round you own).
  It takes a claim on the lane while you write the round (claim_lease,
  default 2h; swim lint refreshes it; it ends when the lane runs or is
  archived): another session's swim new and archive refuse the lane,
  naming who holds it, and swim status shows it to them. --force takes it
  over (logged as claim-broken). A session is SWIM_SESSION, else
  user@host.
  --round-id ID   use your own round id instead of a random UUID: 8-64
                  letters, digits, '.', '_' or '-', not all digits, unique
                  across lanes.
`,
	"run": `swim run [N|ROUND-ID ...] [--rerun] [--plain] [--run-id ID] [--parallel N]
         [--yaml] [--yaml-output] [--interrupt graceful|immediate] [--no-prompt]

  Run lanes. Each argument is a lane number or a round id (full, or 8+
  characters of it). A round id pins the run to that round: it runs the lane
  whose script holds it, and refuses if none does. Named lanes always run,
  even if their round already passed (rounds are rerunnable).
  With no arguments (or "swim all"), runs every lane whose lane.N.sh holds a
  ready round (a Round: line, not a stub) that hasn't passed yet; rounds
  that passed show as "passed (an earlier run)". --rerun runs them too.
  Lanes run in parallel as "bash lane.N.sh", each in its pinned copy.
    - The top of the screen pins one row per lane in use: its state and
      round. Lanes waiting on a dependency show a throbber and
      "waiting on lane N". Output scrolls below, prefixed [N].
      The panel shows at most 15 lanes. With more, a "… N more" line counts
      the hidden ones; empty, ready and passed lanes are hidden first, then
      waiting and failed ones, so running lanes stay visible.
    - Dependencies come from each script's "# After:" line.
      A lane starts when its dependencies in this run pass, and is skipped if
      one fails or is skipped, or if a dependency outside the run holds a
      round that hasn't passed. Cycles stop the run before it starts.
    - With more than one lane, lanes get no stdin (prompts fail closed).
    - Ctrl-C (swim guide running): the first press stops every
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
      stays until you press q ("run ended · q to close"); the summary
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
  --plain   no live view (no pinned panel, interactive view, colour or
            throbber); state changes print as lines. Automatic when stdout
            isn't a terminal or NO_COLOR is set.
  Each lane keeps one colour: sixteen before one repeats on a terminal with
  256 colours (Ghostty, iTerm, VS Code, TERM=*-256color, COLORTERM=truecolor),
  eleven on others. SWIM_COLORS=256 or 16 says which yours is.
  SWIM_TUI=0 keeps the pinned panel without the interactive view (output
            scrolls in the terminal). The interactive view is also off for a
            single lane (it may prompt on stdin), with --plain, --yaml,
            NO_COLOR or SWIM_COLOR=0, CI=true, when stdin or stdout isn't a
            terminal, and when the terminal is too small.
  KEYS (interactive view)
    → l Tab / ← h Shift-Tab   next / previous lane, in panel order (running,
                    then waiting, then ended); from the all view → opens
                    the last lane selected, ← the last lane
    ↑ k / ↓ j       scroll one line      PgUp b / PgDn Space   a page
    Home g / End G  top; bottom and follow again
    4 2 Enter       jump to lane 42
    Enter           open the lane waiting longest for approval (see below)
    y / n           on a lane waiting for approval: approve / decline it
    f               which lanes ← → visit: every lane / running / failed
    Esc             back to the all view, following, filter cleared
    Ctrl-C          interrupt, as without the view   ?  help    q  close
                    the view after the run
  --yaml    stdout is a stream of YAML documents (schema swim.run/v1: run,
            waiting, start, step, finish, then summary)
            instead of the panel and table; lane output goes only to the
            logs. See swim guide logs. --yaml-output also
            streams lane output lines as output events.
  Guard flags are passed through the environment:
    FIN_ALLOW_DELETE_ZG_ITEMS=1 swim run 2
  With a guard's flag unset and swim on a terminal, the lane asks instead:
    lane 2 requires human approval of guard FIN_ALLOW_DELETE_ZG_ITEMS
      round-id 3f2a9c1e-... · manifest 3be9a1c07d2e
      the lane says: delete the zg items - 2026-10-12
    (or set FIN_ALLOW_DELETE_ZG_ITEMS=1 to approve without asking)  Y/N [N]
  The first lines are swim's own: the lane, round id and manifest the launcher
  started (each lane process has its own approval token), not what the
  request claims. A flag neither the script nor a file it includes declares
  or uses is refused (unless the script names a guard's flag at run time,
  guard "$F"), and an approval is used once, by the lane it was given to.
  The lane's panel row shows "waiting for approval".
    - Interactive view: several lanes can wait at once. A banner names them
      ("▶ lane 2 needs approval · Enter to view it and answer"); Enter opens
      the lane waiting longest, showing its log with the question under it,
      and there y approves, n declines. ← → still visit other lanes and Esc
      returns to all lanes; the question waits until answered.
    - Otherwise questions are asked in turn on a line below the output:
      y approves; n, Enter or Esc declines. Lane output is held meanwhile.
  An approval is logged with who answered; a decline makes the step a dry
  run. Ctrl-C declines every waiting question and stops the run as usual.
  Not asked: in CI, without a terminal, with --yaml or --no-prompt (then
  only FLAG=1 approves). To approve without being asked, set the flag up
  front: FLAG=1 swim run N (FLAG=0 declines).
  --no-prompt   answer every guard question no: an unset guard flag is a
                dry run
  Each lane runs a pinned copy of its files (.swim/runs/<run>/<N>/), fixed
  before it waits for anything and checked again right before it starts;
  an edit to the working tree after that changes nothing in the run. See
  swim guide manifest.
`,
	"all": `swim all [--rerun] [--plain] [--run-id ID] [--parallel N]
         [--yaml] [--yaml-output] [--interrupt graceful|immediate] [--no-prompt]

  Run every lane whose ready round hasn't passed yet: the same as
  "swim run" with no lane numbers. Rounds that already passed are shown as
  "passed (an earlier run)" and not run again; failed, interrupted,
  skipped and ready rounds run.
  --rerun   run every ready round, including ones that already passed.
  Every lane script counts, whatever its number. Dependencies ("# After:"
  lines), the live view and its keys (off with --plain), guard flags and
  the summary all work as in "swim run".
  Stubbed lanes and lanes without a Round: line are left alone.
`,
	"status": `swim status [N|ROUND-ID ...] [--yaml] [--rebuild] [--run RUN]

  Show the last state of every lane in use (or just the lanes named, by
  number, round id or script path: swim status 17 18, swim status 2,3,
  swim status lane.17.sh), from .swim/status.yml: state, round id, ready
  round and its round id (from lane.N.sh on disk), last round, counts,
  current or failed steps, and timing. Running lanes whose process is gone
  are shown as "interrupted (its process is gone)"; the next swim run, swim all, swim
  ci or swim watch ends their round as interrupted. Guards the round's run
  approved with no one asked are listed as "approved without a prompt", by
  a guard flag (flag_approved: FLAG=1, a batch's --approve).
  Guards it didn't approve, whose steps ran as dry runs, show as [DRY]
  (dry_run), here, in the live view and the watch screen.
  Under each lane holding a ready round (not one live in a run, nor one
  that already passed), what its header declares and where that stands:
      after lane 1 (ready: a dry run or failure there skips this)
      guard DROP_OK  drop table old_orders
             not approved; swim run will ask (a dry run if no one answers)
      timeout 30m · locks orders-db · includes lib/tables.sh
  Each lane it waits on is passed, ready, running, failed, skipped,
  interrupted or "no round"; each guard is decided as swim run would as
  it starts now (FLAG=1 in this environment), and says whether swim run
  would ask or dry-run. Only what the header sets is shown (Fail: hard, a
  target, an owner other than you too). A lane another session is writing
  shows its claim (swim new took it); your own doesn't.
  --yaml     the status file's fields as YAML, tagged schema: swim.status/v1;
             each ready lane adds pending_header {after, guards: [{flag,
             description, approved, how, asks}], timeout,
             locks, includes, fail, targets, owner} and deps: [{lane, job (its round id),
             state, note}] (deps_error if its After: doesn't resolve);
             claims: the lanes' live claims (swim new takes one), by lane
             (with N|ROUND-ID, just that lane); the file itself is unchanged
             (cat .swim/status.yml)
  --rebuild  reconstruct status.yml from lane scripts and logs
  --run RUN  each lane as it was in swim run RUN (from the logs, archives
             included, and .swim.log for lanes skipped before they started)
`,
	"step": `swim step [options] "<label>" -- cmd [args...]

  In a lane script in any language (swim guide other-languages): run one
  step and record PASS or FAIL <label>; a failure doesn't end the round.
  Options: --timeout D, --retry N, --backoff D, --retry-on CODES,
  --no-retry-timeout, --soft, --expect-fail[=CODES], --key K, --target T.
  (In a bash lane, use the library's step.) The old in-lane form, swim run
  "<label>" -- cmd, still works for one release.
`,
	"gate": `swim gate [options] "<label>" -- cmd [args...]

  In a lane script in any language (swim guide other-languages): run
  one step like swim step; if it fails, record STOP and end the round,
  stopping the program even if it carries on. Options as for swim step.
  (In a bash lane, use the library's gate.)
`,
	"snapshot": `swim snapshot [options] "<label>" -- cmd [args...]

  In a lane script in any language: run a read-only step and save its output
  to .swim/snapshots/laneN-<time>-<label>.txt.
`,
	"guard": `swim guard FLAG "<action - date, reason>"

  In a lane script in any language: exit 0 if the operator approves the
  action, 1 if not. FLAG=1 approves up front; unset, swim asks the
  operator; anything else records SKIP as a dry run.
  Under swim run the launcher decides: FLAG=1 counts only if swim run's own
  environment set it (a FLAG=1 the lane sets is a dry run).
  One flag per destructive action; list it in the header (or lane.N.yml):
  under swim run, a flag the script neither lists nor uses by name is
  refused.
`,
	"stage": `swim stage snapshot|check|change|verify

  In a lane script in any language: start a stage. Out of order or twice
  records WARN; an unknown name ends the round.
`,
	"env": `swim env [N|ROUND-ID] [--yaml]

  The environment env files give lane N (with no lane: the general keys and
  this repo's [repo NAME] and [repo NAME/BRANCH] sections), masked as the lane's logs show it, with where each key comes from
  (file and line) and whether the shell overrides it. Exits 1 if a key is
  one of the lane's guard flags. --yaml: a swim.env/v1 document with the
  env_sha256 the round would record. See swim guide env.
`,
	"archive": `swim archive N|ROUND-ID ... ["<note>"] [--as NAME] [--force]
swim archive --passed ["<note>"]

  Tidy lanes whose rounds have ended: rename each lane's
  .swim/logs/laneN.log to laneN.prev-<NAME>.log in the same directory,
  and replace its lane.N.sh with a "nothing ready" stub carrying <note>
  (default: where the log went). The slot is empty: status shows the lane
  empty with its note, swim run skips it, and swim new N writes the next
  round. swim archive 1 2 3 "shipped" tidies lanes 1, 2 and 3. Every
  argument is a lane number or a round id (the lane whose script holds that
  round or that last ran it); the words after them are the note. NAME is
  slugified; without --as each archive is named after its round id.
  A script edited since its round ran holds new work: its log is archived
  and the script is kept (swim new N --force replaces it).
  --passed every lane whose round passed
  --force  take over another session's claim on the lane (logged as
           claim-broken)
  All or nothing: if any lane is running, has no log, is claimed by
  another session, or its archive name exists, nothing is archived and
  every problem is listed.
`,
	"note": `swim note [--lane N|ROUND-ID] "<text>"

  Append a note to .swim.log, the project log, so the next agent learns it:
  a decision, a finding, why a lane was archived, what to do next.
  --lane N|ROUND-ID   tie the note to a lane (and its round id)
  Keep notes short and factual; never include secret values.
`,
	"log": `swim log [N|ROUND-ID|RUN] [--all] [--full] [--raw] [--yaml] [--follow]
swim log --since <1h|TIME|RUN|ROUND-ID>

  Print logs:
    swim log            the project log, .swim.log (one line per top-level action)
    swim log --since X  what happened since X: the project log's lines from a
                        duration back from now (90m, 1h, 2d), a UTC time
                        (2026-10-15T14:02:11Z), or a run's or round's first line
                        on. Each round's detail is then swim log ROUND-ID
    swim log N          lane N's current log, .swim/logs/laneN.log (every round since its
                        last archive; the newest round is at the end)
    swim log N --all    all of lane N's logs: archives (.swim/logs/laneN.prev-*.log),
                        oldest first, then laneN.log, each under a header
    swim log ROUND-ID   just that round (full id or 8+ characters),
                        from whichever current or archived log holds them
    swim log RUN        every lane's round from that swim run, in lane order
  On a terminal the log is rendered: round and stage headers stand out,
  results are coloured, times show how far into the round they were, and
  step output longer than 40 lines is folded to its first and last 15.
  Either way, repeated output is folded: a line said three or more times
  in a row shows once, then "… N more times"; lines that differ only in
  their numbers never fold. A line a progress bar or spinner
  redrew with carriage returns shows what the terminal was left showing,
  and a spinner frame alone is dropped. The file keeps every line.
  Piped (swim log 2 | less) it is the file's lines, folded so, plus the step
  running now: a step's block reaches the log file only when it ends, so
  swim log adds it from the step's spool (.swim/logs/laneN.log.partial),
  output so far included, under a result line that never appears in a file:
      RUNNING  <label> (in progress)          <elapsed>s  <hh:mm:ss>
  ("cut off: ..." instead of "in progress" if swim died mid-step; it is
  recorded as FAIL when the lane next runs).
  --full        don't fold long or repeated output
  --raw         print the file exactly, as cat shows it, even on a terminal
                (nothing folded, no running step)
  --yaml        the round parsed, as data (schema swim.log/v1): the latest round,
                every round with --all, or a round id's; the running step is
                a result "RUNNING" with its command, output so far and
                duration_s. See swim guide logs.
  --follow      print lane N's (or ROUND-ID's lane's) current round, then follow it
                like tail -f: new log lines, and the running step's output as
                it is written (the step's block then adds only its result
                line). It stops by itself once the round has ended (on an
                lane at rest, at once, after its last round; with no round yet,
                it waits for one). To wait for a round's result, and exit
                with it: swim wait N.
`,
	"ci": `swim ci [N|ROUND-ID ...] [--changed[=BASE]] [--rerun] [--junit FILE] [--provider github|gitlab|generic]
        [--require-work] [--heartbeat D] [--run-id ID]

  Run rounds in a CI job: swim all, written for a CI log viewer and built for
  commits. Detects GitHub Actions (GITHUB_ACTIONS), GitLab CI (GITLAB_CI) or
  any runner (CI=true); --provider overrides. Works outside CI too (generic).
    - Header: swim version, provider, commit and branch, the lanes selected
      and why, and the guard flags set (names only, never values).
    - Live: plain output, every lane prefixed [N], step headers and results
      included; "[N] still running: <step> (5m 2s)" for a lane silent for
      --heartbeat (default 60s; 0 turns it off).
    - After the run: each lane's round log in its own section (::group:: on
      GitHub, collapsed sections on GitLab; failed lanes left open),
      annotations (FAIL/BLOCKED/STOP error, a dry-run SKIP notice, one
      notice for the lanes skipped below a failure), the
      Markdown job summary ($GITHUB_STEP_SUMMARY), --junit FILE (one suite
      per lane, one case per step), and the paths to upload as artifacts.
    - Never interactive: lanes get no stdin (confirm fails closed), no
      prompts.
    - Pinned: the selected rounds' round ids are fixed up front; a lane
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
                    every ready round. Needs the history (fetch-depth: 0).
  --rerun           also rounds that already passed
  --require-work    exit 1 when there is nothing to run (default: a notice, 0)
  Exit codes: 0 every selected lane passed, 1 a lane failed or was skipped,
  2 usage, config or an unresolvable # After:.
  A runner has no config of its own: swim's defaults apply (see CONFIG);
  commit the lane scripts the job runs (git add -f lane.N.sh, or drop
  lane.[0-9]* from swim's .gitignore block). swim never commits or pushes
  results: upload .swim/logs/, .swim/snapshots/, .swim.log and the JUnit file
  as artifacts. "Already passed" lives in .swim/status.yml: a fresh runner
  reruns every ready round unless --changed narrows it (or .swim/ is
  restored from a CI cache).
`,
	"queue": `swim queue [list] [--all] [--yaml]
swim queue N|ROUND-ID ... [--approve FLAG[,FLAG]]... [--parallel N] [--note TEXT]
swim queue add [flags]           every ready round that hasn't passed
swim queue show|rm|requeue ID

  Queue a batch of rounds to run as soon as its lanes are free, by a
  watcher (swim watch). Get a batch ready, queue it with its approvals, and let it
  fire when the current work is done.
  add       queue the named lanes, or with none every ready round that
            hasn't passed (as swim all would run). The batch is fixed now:
            each lane is pinned to the round it holds, its script, its
            manifest (what it sources, its sidecar, how it runs) and
            its env, so a lane changed before the batch runs is skipped, not
            run.
            --approve FLAG  approve that guard for this batch (repeat, or
                            FLAG,FLAG); it must be a guard of a lane in the
                            batch, so a typo fails here, not at 3am
            It prints the batch's id; swim wait ID blocks until that batch
            ends and exits with its result.
            Guards a batch doesn't approve are asked on swim watch's screen
            (on a terminal, running batches side by side), naming the lane;
            with no such watcher they are dry runs. A guard flag in the
            watcher's own environment does not count (the queue entry alone
            says what may change).
            add says which: with a watcher up whose screen asks, it names
            it; with none, a batch that approves none of its lanes' guards
            warns on stderr,
            naming each lane and the guards that will dry-run.
            Watchers say which swim they are (.swim/workers/): with watchers
            up and none able to run the batch (its swim reads an older
            syntax than a lane's script), add refuses, naming each
            watcher's swim and this one; with no watcher up it queues and
            warns. The entry records the swim that queued it (swim).
  list      waiting, running and orphaned entries, and the last 5 consumed
            (--all: every one); --yaml for scripts (schema swim.queue-list/v1).
            A batch a watcher leaves queued says why ("left queued: ...")
  show ID   the entry file; ID is its id, a 4+ character prefix, or #seq
  rm ID     remove an entry that isn't running
  requeue ID  put an orphaned (its watcher died) or consumed entry back at the
            end of the queue
  A claimed entry is orphaned when its watcher is gone: on the watcher's host,
  its process (pid and start time) is; from another host sharing the queue,
  its heartbeat is older than its lease (queue_lease, default 2m). A claim by
  an older swim (no heartbeat) can't be judged from another host and shows
  as running? (can't tell).
  Entries are files under .swim/queue/: NNNN-q-<utc>-<hex>.queue while
  waiting, .running once a watcher has claimed it (by renaming it, which
  is atomic: two watchers can never claim the same entry), then
  done/NNNN-....yml with its result (run id, exit, each lane's result).
  .swim.log records queue, queue-run and queue-done events.
`,
	"watch": `swim watch [--once] [--interval D] [--batches N] [--plain]

  The queue's watcher. Runs queued batches (swim queue N) side by side,
  up to --batches at once (watch_batches, default 4), each in a process
  of its own: a batch starts as soon as none of its lanes is in another
  batch or another swim run, oldest first, so a later batch whose lanes
  are free starts ahead of one that has to wait. Lanes that depend on
  (After:) or share locks with a lane of another batch wait for it as
  they do across swim runs. It runs each batch like swim run with the
  batch's lanes pinned and its approvals, never prompts, and consumes the
  entry when the run ends (passed, failed or interrupted alike: a half-run
  change is never retried on its own; swim queue requeue does that).
  Several watchers can share a queue: claims are atomic, and a watcher
  picks batches under a lock, so two never take the same lanes. A batch
  submitted on a watcher's screen (s, Q) starts on that watcher when its
  lanes are free, so Ctrl-C there is what stops it. While a
  batch runs, its process renews the claim's heartbeat every third of
  queue_lease (swim config). A batch this watcher's swim can't run (a
  lane's script is newer than it reads) is left queued for a watcher that can, with why, in swim queue and
  .swim/workers/<host>-<pid>.yml, which says which swim the watcher is. As
  it starts, the
  watch ends the round of any lane whose process died without ending it
  (as swim run does): END INTERRUPTED in its log, interrupted in status.
  --once         run every queued batch, then exit (non-zero if one failed)
  --interval D   how often to check the queue and whether anything runs (default 2s)
  --batches N    run up to N batches at once (default watch_batches, 4);
                 --batches 1 runs one at a time, on the screen.
  --plain        print what the watcher waits for instead of the lanes
                 screen (automatic when stdin or stdout isn't a terminal)
  On a terminal, while it waits, the watch shows every lane's round and the
  queue, with the batches running: ↑ ↓ (or k j) pick one, r runs it now
  and enter follows its output (esc comes back). q adds a lane to the next
  batch (q again takes it out) and s submits the batch, as swim queue N
  N M does: one run, so After: between its lanes holds; x clears it.
    enter on a lane follows its log while it runs, in a batch or not;
                   v switches to its batch's output. When it ends, a header
                   bar says how: PASSED (green), FAILED (red, with the
                   failed steps) or INTERRUPTED, with the exit code and
                   time; enter on a batch does the same for the batch
    r on a lane    runs it as swim run N does: you're at the terminal, so its
                   guards may ask
    q on a lane    queues it as swim queue N does (the batch approves none
                   of its guards); the watcher takes it when its lanes are free
    a on a lane    archives it as swim archive N does: the lane goes
                   empty, the log is kept as laneN.prev-<round-id>.log and the
                   script becomes a stub (not while it runs, nor without a
                   log)
    r on a batch   starts a waiting batch now, out of turn, if its lanes are
                   free, with its own approvals, exactly as in turn
    enter on a batch  follows its output (.swim/queue/out/<seq>-<id>.log);
                   v switches to the verbose output (its lanes' logs, the
                   running step's output included) and back
    l on a lane    its current log, every round (v: the last one),
                   running or not; followed while it runs
    l on a batch   its lanes' latest rounds, from their last run while it
                   waits, as they go while it runs (v: the batch's output)
    o              hide or show the running output: under the queue, the
                   latest lines of every running lane's log, the running
                   step's output included (shown by default)
  [DRY] marks a lane whose round dry-ran a guard, and a waiting batch with
  a guard it doesn't approve that no screen will ask for.
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
  own with --plain; swim watch --once (from cron, say) runs what's queued
  and exits.
`,
	"wait": `swim wait [--interval D] [--timeout D]
swim wait [--any] N|BATCH|RUN ... [--interval D] [--timeout D]

  The one command that blocks until work is done.
  With lanes, batches or runs named, wait for those alone, whatever else
  runs, then print how each ended and exit as they went: 0 passed, 3 when
  one dry-ran a guard and none failed, 1 otherwise.
    N        a lane (its number, script path or task): the round it is
             running, or the one a swim run holds it waiting to start; on an
             lane at rest, its last round (with none yet, it waits for one)
    BATCH    a queued batch: its id as swim queue printed it, 4+ of its
             first characters, or '#41'; a round id names the newest batch
             that queued it. A batch orphaned by a dead watcher counts as
             ended (it never runs by itself).
    RUN      a swim run id (8+ characters): every lane of that run
  A round id no batch queued names the lane that holds or last ran it.
  An agent queues work and waits for it: swim queue 3, then swim wait
  q-20260101T120000Z-ab12 (waiting on the lane instead could read its
  previous round, before a watcher takes the batch).
  With nothing named, block until nothing runs and nothing new is about
  to start: no lane running, no swim run waiting on lanes, and no batch
  queued or running (an orphaned batch never runs, so it doesn't count).
  With batches queued and no watcher up, it waits until one starts (swim
  watch). Prints what it is waiting on whenever that changes.
  --any          return when the first of them ends, with its result: an
                 agent with several batches out reads that one, then
                 waits on the rest again
  --interval D   how often to check (default 2s)
  --timeout D    give up after D (exit 1, saying what is still busy)
`,
	"verify": `swim verify [--strict] [--allow-unsigned] [--force] [--yaml]

  Prove the chain of custody (.swim/chain.jsonl) against itself. It prints
  one answer, VERIFIED or NOT VERIFIED (exit 1), with what failed. It
  checks that every entry follows the one before it (sequence and
  previous-hash: a changed, removed or reordered entry breaks every link
  after it); every signature is valid for the key recorded beside it;
  every round's log section still hashes to what was recorded, the copy
  of its script kept under .swim/scripts/<sha256>.sh is unchanged, and
  its pinned copy (.swim/runs/<run>/<N>/, until swim doctor --fix prunes
  it) still matches the manifest it ran; and every approval a run
  recorded is in its run's attestation. BROKEN means something changed.
  A chain replaced wholesale and re-signed with another key, or with
  entries removed from its end, verifies too.
  Unsigned entries after the last signed one fail as UNSIGNED (nothing
  vouches for them, and a chain rewritten with no key looks the same)
  unless --allow-unsigned; ones a later signed entry links to (a repo that
  gained a key later) are a warning, since they can't change unseen.
  Exits 1 when anything is BROKEN or an entry is unsigned (without
  --allow-unsigned); --strict also on any warning.
  --force acknowledges the gaps and deletions it finds: entries missing,
  inserted or out of order, a link that doesn't follow, a round's log or
  kept script deleted. It appends one amend entry listing them, signed
  with your key, and from then on swim verify shows each as AMENDED (a
  warning, which --strict counts), naming who acknowledged it: you, not
  whoever recorded the entries. Changed content (a signature, log, script
  or pinned copy that doesn't match) is never amended, and a gap that
  appears after the amendment is BROKEN again.
  --yaml               the result as data (schema swim.verify/v1):
                       consistent ("yes" or "no"), ok (what the exit code
                       says), broken, unsigned, amended, warnings, notes.
`,
	"signoff": `swim signoff verify FILE ...

  A lane with # Signoff: CONTEXT is local CI: when its round passes, swim
  (not the lane) signs a statement with the key of whoever runs the
  launcher (swim run, swim ci run locally, or the watcher under swim
  watch; never swim ci in a CI job): the commit and its
  tree, the lane, its script and manifest hashes, the round id and run. It
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
  verify checks each file's signature, that this machine's signing key
  (signing_key, or ~/.ssh/id_ed25519) made it, and that its commit (if
  this repo has it) still has the tree it names; exits 1 if any fails.
`,
	"chain": `swim chain repair

  Look after the chain of custody file itself (.swim/chain.jsonl).
  repair       remove a half-written final entry: a write cut short by a
               crash, a kill or a full disk leaves a last line with no
               newline, and swim refuses to append after it (and swim verify
               reports it). repair, under the chain's lock, moves those
               bytes to .swim/chain.tail-<time>.bak, truncates the chain
               after its last complete entry, and appends a signed repair
               entry recording how many bytes were removed and their
               SHA-256. Complete entries are never touched; with nothing to
               repair it changes nothing. swim verify notes the repair
               entry.
`,
	"keys": `swim keys

  The key this machine signs the chain and signoffs with (signing_key, or
  ~/.ssh/id_ed25519): its fingerprint, path and comment, and the signing
  mode (signing: auto, required or off).
`,

	"interrupt": `swim interrupt N|ROUND-ID

  Ask one running lane to stop at its next step boundary, from any
  terminal: its current step finishes, no further step starts, and the round
  ends interrupted (STOP  interrupted by operator (after: <step>), exit 130).
  Other lanes keep running. The same thing the first Ctrl-C does to every
  lane of a swim run.
`,
	"lint": `swim lint [N|ROUND-ID ...] [--strict] [--yaml] [--fix]

  Check lane scripts before they run; changes nothing (but --fix). With no lanes, lints
  every lane script (stubs skipped). Each finding is printed as
      lane.2.sh:5  error  <what is wrong>  [code]
                          fix: <how to fix it>
  then a summary line ("2 errors, 1 warning"). Exit 1 on any error; with
  --strict (for CI), on warnings too. --yaml prints a swim.lint/v1 document.
  --fix adds the header lines a script's body needs, for you to review:
  # Guards: for each guard it uses without declaring (with the guard's own
  action as the description), # Guards: * for a flag it names at run time,
  # Includes: for a repo file it sources or imports. In a bash lane it
  also renames run, step's old name, to step. It never removes a line or
  rewrites one otherwise, and skips running lanes and lanes of jobs. It
  also rewrites a # Job: line, Round-ID's old name, to # Round-ID:.

  Checks [code] (level):
    round (error)          no # Round: line
    placeholder (error)    a template placeholder left in
    after (error)          # After: names an unknown lane or round id, or itself
    cycle (error)          lanes wait on each other in a cycle
    set-e (error)          set -e / set -o errexit (failed checks must go on)
    lane-init (error)      no lane_init, or lane_init with another lane's number
    summary (warn)         summary missing or not the last line
    stage (error)          an unknown stage name (the round would stop there)
    stage-order (warn)     stages out of order, repeated, or change first
    bash4 (warn)           declare -A, mapfile, ${x,,}, |&, coproc, ... (macOS
                           has bash 3.2)
    gnu-only (warn)        sed -i with no suffix, grep -P, date -d, stat -c,
                           find -printf, timeout: a Mac's BSD tools don't run
                           them as written (swim doctor says if this one's do)
    guard-unlisted (error) a guard flag used but not declared in # Guards:
                           (refused when it asks; a warning with # Guards: *)
    guard-dynamic (error)  guard "$FLAG" without # Guards: *
    guard-unused (warn)    a # Guards: flag no guard uses
    run-is-step (warn)     a call of run, step's old name (works for this
                           release only; --fix renames it)
    job-is-round-id (warn) a # Job: line, Round-ID's old name (--fix renames
                           it)
    destructive (warn)     delete/destroy/drop/rm -r outside an if guard block
    blocked (error)        a blocked_commands pattern
    secret-echo (warn)     echo/printf of a secret variable
    header (warn)          a Timeout, Step-Timeout or Locks value swim ignores
    env (error)            the lane's env files don't parse, or set its guard flag
    fail (error)           Fail: without syntax 3
    includes (error)       a Python/TypeScript lane imports a local module its
                           # Includes: doesn't list (it isn't staged: the
                           import fails under swim run)
    source (error)         a bash lane sources a local file its # Includes:
                           doesn't list (the round stops there under swim
                           run), or a path built at run time (source "$x"),
                           which swim can't pin or stage
    check-unsafe (warn)    a destructive-looking command (terraform apply, a
                           delete) before stage change, in a stage that
                           should only read
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

  Check what running swim on this machine needs: one line per finding,
  its fix below, then the counts (0 errors, 0 warnings when all is well).
  Exits 1 on any error (--strict: on warnings too; --yaml: a swim.doctor/v1
  document). Run it where lanes run (the operator's machine): the bash and
  tool checks are about this machine's. Checks [code] (level):
    binary-platform (error)   this swim, bin/swim or a swim on PATH is built
                              for another OS/CPU (read from its ELF/Mach-O
                              header; nothing is run)
    path-missing (warn)       no swim on PATH
    swim-bin (warn)           $SWIM_BIN, or the first swim on PATH, isn't this one
    config-parse (error)      the config file doesn't load
    config-key (warn)         config.yml sets a key swim doesn't read (a
                              typo, a removed setting): named with its line
    config-repo (warn)        a config.yml repos: key or env.swim [repo]
                              section that's a path; key repos by name
    git (error)               git missing, or not inside a git repo
    bash (error/warn)         no bash on PATH (error); bash older than 4
                              here while a lane uses a bash 4 feature: each
                              lane and line (warn)
    gnu-only (warn)           not on Linux: a lane line using a GNU-only
                              form (sed -i, grep -P, date -d, stat -c, find
                              -printf, timeout) this machine's tool doesn't
                              run (swim lint's gnu-only, checked against the
                              tools here: GNU ones on PATH are fine)
    toolchain (error)         config toolchain fails in bash -c (10s limit)
    notify (warn)             notify_url doesn't answer (5s), curl is
                              missing, or notify_command's program isn't on
                              PATH
    notify-failing (warn)     the last notification sent failed: swim
                              config, then t, sends a test
    gitignore (warn)          swim's .gitignore block missing or out of date
    tracked (warn)            git tracks .swim/, or lane scripts in a repo
                              whose CI doesn't run swim ci
    env-file (error)          an env file that doesn't parse, or sets a
                              lane's guard flag
    stale-pid (warn)          .swim/laneN.pid naming a process that is gone
    claim-expired (warn)      a lane claim in .swim/claims/ past its lease
    watch-gone (warn)         a swim watch's .swim/workers/ file whose
                              process is gone (this host) or whose heartbeat
                              stopped (another host)
    leftover (warn)           a file under .swim/ swim no longer reads
                              (hub.yml, headless.*, groups/, observations/,
                              out/, checkpoints/, earmarks/, lane1.2.claim,
                              ...)
    stages-old (info)         runs' staged lane copies (.swim/runs/<run>/)
                              older than 14 days, of no lane's latest round
    remote-running (warn)     a lane has run on another machine sharing the
                              repo for over a day; if that machine is gone,
                              release it (rm .swim/laneN.pid, then swim
                              status --rebuild)

  --fix applies only safe local repairs, then checks again: it rewrites
  swim's .gitignore block, and deletes stale pid files, expired claims,
  gone watches' files, old pinned copies and the leftover files above,
  checking each again first. It never edits config, lane scripts or git,
  and never deletes lane logs, the chain, status.yml, the queue,
  .swim/scripts, snapshots or signoffs.
`,
	"changelog": `swim changelog [-n N] [--since VERSION] [--all]

  What changed in swim, from what was built into this binary. Run it after
  updating to see what is new. Works anywhere; it needs no repo.

  swim changelog                  the last 5 commits this swim was built
                                  from: each one's subject, the areas it
                                  changed (internal/cli (5), docs (2)) and
                                  its message's first paragraph. A build
                                  that isn't stamped with them (go build,
                                  the Nix flake: just build stamps them)
                                  shows the newest revision instead
  swim changelog -n 1             the newest revision of CHANGELOG.md, the
                                  release notes (Unreleased on a build from
                                  main)
  swim changelog -n 3             the newest three revisions
  swim changelog --since 0.20261014.1
                                  every revision after that version (or tag,
                                  v0.20261014.1; an older 7.20261013; or a
                                  pre-release commit id)
  swim changelog --all            every revision

  A revision is one "## " section: Unreleased (on a build from main), a
  release version 0.<YYYYMMDD>.<index> (earlier, <breaking>.<YYYYMMDD>), or
  a commit for revisions before versioning. A revision that bumps the syntax
  version has Upgrading notes: read them before running lanes.
`,
	"skill": `swim skill

  How to install the Claude skill for swim: where it lives (skills/swim in
  swim's repo, at this binary's release tag, else main) and the line to
  paste into a Claude Code session to install it. The skill teaches Claude
  to read this guide, write and lint lanes, queue them for the operator to
  approve, and read results back. Works anywhere; it needs no repo.
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

  The version says nothing about compatibility: the syntax version does.
  Lane scripts (# swim: syntax 4) and lane logs (# swim log | syntax 4)
  say theirs in the file. swim reads every older one as it is, and
  refuses a file newer than it reads, asking you to update swim. A
  release that bumps the syntax has Upgrading notes (swim changelog).
`,
	"help": `swim help [command]

  Print the overview (the full guide when stdout isn't a terminal), or one
  command's detail.
`,
	"guide": `swim guide [TOPIC|all]

  Print swim's reference (docs/reference in swim's repo, carried by the
  binary): with no topic, the index of topics; with one (swim guide lanes),
  that page; with all (or swim --help --full), every page and every
  command's detail.
`,
}

// For is a command's help.
func For(name string) (string, bool) {
	h, ok := Commands[name]
	return h, ok
}

// Full is the complete --help text: the guide plus every command's detail.
// FullOrder is the order of the command details in swim guide; every
// command in Commands is in it (help_test checks).
var FullOrder = []string{"init", "config", "env", "new", "run", "all", "status", "log",
	"step", "gate", "snapshot", "guard", "stage",
	"archive", "note", "changelog", "version", "skill", "interrupt", "queue", "watch", "wait",
	"verify", "chain", "keys", "ci", "signoff", "lint", "doctor", "guide"}

// Short is swim --help on a terminal: the commands most people need, one
// line each, and where the rest is.
func Short() string {
	return `swim: an operator-in-the-loop runner for ops work

  Your AI writes lanes. You run them, with your access.
  Guards wait for you. The log carries the results back.

  lane     a piece of work: a numbered script, lane.N.sh, your AI writes
  round    one go at that work, run by you, with your access
  guard    a step that needs your approval: a dry run until you give it
  log      what happened, step by step, for the AI to read back

USAGE
  swim                         in a swim repo: what needs you, and what's running
  swim <command> --help        one command's detail
  swim guide [TOPIC]           the reference, one topic at a time (people and AI agents)

LANES
  init                         set up this repo
  new N "<round goal>"         write lane.N.sh from the template
  lint [N]                     check lane scripts before they run
  A lane script calls stage, step, snapshot, gate and guard.

ROUNDS
  run N ...                    run lanes: guarded steps are dry runs until approved
  all                          run every ready lane, dependencies first
  queue N ...                  queue a batch to run once its lanes are free
  watch                        the watcher: run queued batches as lanes free up

GUARDS
  run N                        on a terminal, swim asks you at each guard
  FLAG=1 swim run N            approve one guard up front (the log says so)
  queue N --approve A          approve ahead, for one batch pinned to this exact script

LOGS
  status [N]                   every lane's state, and why
  log [N|ROUND-ID|RUN]         a lane's log, step by step
  log --since 1h               what happened since a time, run or round

EVERYTHING ELSE (swim <command> --help, or swim guide commands)
  verify, keys, chain          the record of approvals and rounds
  doctor, config, env          the environment lanes get
  archive, note                tidy up and record
  interrupt, wait              control running lanes
  ci, signoff, changelog, version, skill
`
}

func Full() string {
	var b strings.Builder
	for _, t := range Topics {
		b.WriteString(page(t) + "\n")
	}
	b.WriteString("# Command detail\n")
	for _, order := range FullOrder {
		b.WriteString("\n" + Commands[order])
	}
	return b.String()
}
