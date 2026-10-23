package cli

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim pause holds a running lane before its next step; swim unpause (or
// swim resume, the same command) lets it go on. The round stays alive the
// whole time: its process, staged script, approvals, slot and locks. A
// pause ends nothing and never expires; swim interrupt (or the first
// Ctrl-C of its run) ends a paused round as it ends any other.

// pausePoll is how often a held round looks at its pause request.
var pausePoll = 200 * time.Millisecond

// holdIfPaused is the hold at a step boundary: if lane n's round in run
// is paused, it records PAUSE, marks the lane paused in status.yml and
// waits until the pause is lifted (RESUME) or the round is asked to stop.
// next is the step it holds before. A handler's steps and a read-only
// check never hold.
func holdIfPaused(root string, n int, run, next string) {
	if n <= 0 || os.Getenv("SWIM_IN_HANDLER") != "" || lane.CheckMode() {
		return
	}
	req, ok := lane.Paused(root, n, run)
	if !ok || lane.StopRequested(root, n, run) {
		return
	}
	cell := lane.CellOf(n)
	logPath := os.Getenv("STEP_LOG")
	if logPath == "" {
		logPath = lane.Log(root, n)
	}
	cfg, _ := loadCfg(root)
	lanes := 0
	if cfg != nil {
		lanes = cfg.Lanes
	}
	after := ""
	if st, _ := status.Load(root); st != nil {
		if l := st.Get(n); l != nil {
			after = l.CurrentStep
			if cell != "" {
				if c := l.FindCell(cell); c != nil {
					after = c.CurrentStep
				}
			}
		}
	}
	began := time.Now()
	label := "by " + or(req.By, "?")
	if after != "" {
		label += fmt.Sprintf(" after %q", after)
	}
	detail := ""
	if next != "" {
		detail = fmt.Sprintf("next: %q", next)
	}
	if req.Note != "" {
		detail = strings.TrimSpace(detail + "; " + req.Note)
	}
	line := logparse.MarkLine(logparse.Pause, label, detail)
	appendLog(logPath, line+"\n")
	fmt.Fprintln(os.Stderr, strings.TrimLeft(line, " "))
	updatePaused(root, n, cell, lanes, func(l *status.Lane) {
		l.PausedAt, l.PausedBy, l.PausedAfter, l.NextStep, l.PauseNote = status.Now(), req.By, after, next, req.Note
	})

	stopped := false
	for {
		time.Sleep(pausePoll)
		if lane.StopRequested(root, n, run) {
			stopped = true
			break
		}
		if _, still := lane.Paused(root, n, run); !still {
			break
		}
	}
	held := time.Since(began)
	by := ""
	if data, err := os.ReadFile(lane.ResumedPath(root, n)); err == nil {
		by = strings.TrimSpace(string(data))
	}
	if !stopped {
		label := "by " + or(by, "?")
		line := logparse.MarkLine(logparse.Resume, label, "paused "+display.Elapsed(held))
		appendLog(logPath, line+"\n")
		fmt.Fprintln(os.Stderr, strings.TrimLeft(line, " "))
	}
	updatePaused(root, n, cell, lanes, func(l *status.Lane) {
		l.PausedAt, l.PausedBy, l.PausedAfter, l.NextStep, l.PauseNote = "", "", "", "", ""
		l.PausedS += held.Seconds()
		l.Pauses = append(l.Pauses, status.PauseSpan{At: began.UTC().Format(time.RFC3339Nano), S: math.Round(held.Seconds()*1000) / 1000})
	})
}

// updatePaused updates the paused fields of lane n, or of its cell.
func updatePaused(root string, n int, cell string, lanes int, fn func(*status.Lane)) {
	if cell == "" {
		status.Update(root, n, lanes, fn)
		return
	}
	if n > lanes {
		lanes = n
	}
	status.UpdateFile(root, lanes, func(f *status.File) error {
		fn(f.Mine(n).CellEntry(cell))
		return nil
	})
}

// pausedSoFar is how long lane n's round (or its cell) has been paused in
// all, which its Timeout: deadline moves out by.
func pausedSoFar(root string, n int) time.Duration {
	if root == "" || n <= 0 {
		return 0
	}
	st, _ := status.Load(root)
	if st == nil {
		return 0
	}
	l := st.Get(n)
	if l == nil {
		return 0
	}
	if cell := lane.CellOf(n); cell != "" {
		if c := l.FindCell(cell); c != nil {
			return time.Duration(c.PausedS * float64(time.Second))
		}
	}
	return time.Duration(l.PausedS * float64(time.Second))
}

// roundDeadline is the round's Timeout: deadline (SWIM_DEADLINE, epoch; the
// zero time for none), moved out by the time it has been paused.
func roundDeadline(root string, n int) time.Time {
	return deadlineOf(root, n, os.Getenv("SWIM_DEADLINE"))
}

// deadlineOf is roundDeadline for a lane whose SWIM_DEADLINE is env (the
// lane socket's, which serves a program with its own environment).
func deadlineOf(root string, n int, env string) time.Time {
	s, err := strconv.ParseInt(env, 10, 64)
	if err != nil || s <= 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).Add(pausedSoFar(root, n))
}

// cmdPause is swim pause N|JOB ... | --all [--wait] [--note TEXT].
func cmdPause(args []string) error {
	var all, wait bool
	var note string
	rest, err := flags{bools: map[string]*bool{"all": &all, "wait": &wait}, strs: map[string]*string{"note": &note}}.parse(args)
	if err != nil {
		return err
	}
	if all == (len(rest) > 0) {
		return usagef("usage: swim pause N|JOB ... [--wait] [--note TEXT] | swim pause --all [--wait] [--note TEXT]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	by := operator()
	st, _ := status.Load(root)
	var ns []int
	if all {
		if err := lane.RequestRepoPause(root, lane.PauseReq{By: by, Note: note}); err != nil {
			return err
		}
		history.Log(root, history.Entry{Event: history.PauseAll, Detail: strings.TrimSpace("by " + by + "  " + note)})
		fmt.Println("paused the repo: no lane starts another step, no swimmer claims a batch, until swim unpause --all")
		for _, n := range lane.Associated(root) {
			if l := activeLane(root, st, n); l != nil {
				ns = append(ns, n)
			}
		}
	} else {
		for _, a := range rest {
			n, err := laneRef(root, cfg, a, true)
			if err != nil {
				return err
			}
			ns = append(ns, n)
		}
	}
	var failed bool
	for _, n := range ns {
		if all {
			if l := activeLane(root, st, n); l != nil {
				fmt.Println(pausingText(root, n, l))
			}
			continue
		}
		msg, err := pauseOne(root, st, n, by, note)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			failed = true
			continue
		}
		fmt.Println(msg)
	}
	if wait {
		for _, n := range ns {
			waitPaused(root, n)
		}
	}
	if failed {
		return exitError{1}
	}
	return nil
}

// pauseOne asks lane n to hold before its next step, and says where it
// will.
func pauseOne(root string, st *status.File, n int, by, note string) (string, error) {
	l := activeLane(root, st, n)
	if l == nil {
		return "", fmt.Errorf("swim %d isn't running", n)
	}
	if err := lane.RequestPause(root, n, lane.PauseReq{Run: l.Run, By: by, Note: note}); err != nil {
		return "", err
	}
	history.Log(root, history.Entry{Event: history.Pause, Lane: n, Job: l.Job, Run: l.Run, Detail: strings.TrimSpace(fmt.Sprintf("after %q  %s", l.CurrentStep, note))})
	return pausingText(root, n, l), nil
}

// unpauseOne lifts lane n's pause, if it has one, and says so.
func unpauseOne(root string, st *status.File, n int, by string) (string, bool) {
	_, repoPaused := lane.RepoPause(root)
	if _, ok := lane.LanePause(root, n, ""); !ok {
		msg := fmt.Sprintf("swim %d isn't paused", n)
		if repoPaused {
			msg += " on its own; the repo-wide pause holds it (swim unpause --all)"
		}
		return msg, false
	}
	resume(root, n, by)
	var l *status.Lane
	if st != nil {
		l = st.Get(n)
	}
	job, run, msg := "", "", fmt.Sprintf("swim %d: resumed", n)
	if l != nil {
		job, run = l.Job, l.Run
		if l.PausedAt != "" {
			if t, err := time.Parse(time.RFC3339, l.PausedAt); err == nil {
				msg += " (paused " + display.Elapsed(time.Since(t)) + ")"
			}
			if l.NextStep != "" {
				msg += fmt.Sprintf("; next step %q", l.NextStep)
			}
		}
	}
	history.Log(root, history.Entry{Event: history.Unpause, Lane: n, Job: job, Run: run, Detail: "by " + by})
	if repoPaused {
		msg += "; the repo-wide pause still holds it (swim unpause --all)"
	}
	return msg, true
}

// activeLane is lane n's status entry if its round is in flight (running,
// or waiting for its turn in a run), else nil.
func activeLane(root string, st *status.File, n int) *status.Lane {
	if _, ok := lane.Running(root, n); ok && st != nil {
		if l := st.Get(n); l != nil {
			return l
		}
	}
	if st == nil {
		return nil
	}
	l := st.Get(n)
	if l == nil || !l.Alive() {
		return nil
	}
	switch l.State {
	case status.Running, status.Waiting, status.Queued, status.Locked:
		return l
	}
	return nil
}

// pausingText says where lane n will hold.
func pausingText(root string, n int, l *status.Lane) string {
	if l.State != status.Running {
		return fmt.Sprintf("swim %d: paused before it starts (it's %s); swim unpause %d lets it start", n, l.State, n)
	}
	cur := l.CurrentStep
	if cur == "" {
		return fmt.Sprintf("swim %d: pausing before its next step; swim unpause %d resumes it", n, n)
	}
	running := ""
	if sp, ok := step.ReadSpool(lane.Log(root, n)); ok {
		running = ", running " + display.Elapsed(sp.Elapsed())
	}
	return fmt.Sprintf("swim %d: pausing after %q (in progress%s); no further step will start until swim unpause %d", n, cur, running, n)
}

// waitPaused waits until lane n holds (or its round has ended).
func waitPaused(root string, n int) {
	for {
		st, _ := status.Load(root)
		if st != nil {
			if l := st.Get(n); l != nil && (l.PausedAt != "" || !l.Alive() && l.State != status.Running) {
				return
			}
		}
		if _, ok := lane.Running(root, n); !ok {
			if st == nil || st.Get(n) == nil || st.Get(n).State != status.Waiting {
				return
			}
		}
		time.Sleep(pausePoll)
	}
}

// cmdUnpause is swim unpause (and swim resume) N|JOB ... | --all.
func cmdUnpause(args []string) error {
	var all bool
	rest, err := flags{bools: map[string]*bool{"all": &all}}.parse(args)
	if err != nil {
		return err
	}
	if all == (len(rest) > 0) {
		return usagef("usage: swim unpause N|JOB ... | swim unpause --all   (swim resume: the same)")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	by := operator()
	if all {
		_, was := lane.RepoPause(root)
		lane.ClearRepoPause(root)
		n := 0
		for _, ln := range lane.Associated(root) {
			if _, ok := lane.LanePause(root, ln, ""); ok {
				resume(root, ln, by)
				n++
			}
		}
		if !was && n == 0 {
			fmt.Println("nothing is paused")
			return nil
		}
		history.Log(root, history.Entry{Event: history.UnpauseAll, Detail: "by " + by})
		fmt.Printf("unpaused the repo%s: swimmers claim batches again\n", map[bool]string{true: fmt.Sprintf(" and %d lanes", n), false: ""}[n > 0])
		return nil
	}
	st, _ := status.Load(root)
	for _, a := range rest {
		n, err := laneRef(root, cfg, a, true)
		if err != nil {
			return err
		}
		msg, _ := unpauseOne(root, st, n, by)
		fmt.Println(msg)
	}
	return nil
}

// resume lifts lane n's pause, leaving who did it for the round to record.
func resume(root string, n int, by string) {
	os.WriteFile(lane.ResumedPath(root, n), []byte(by+"\n"), 0o644)
	lane.ClearPause(root, n)
}

// pausedRefusal says why swim run won't start over a paused lane, or "".
func pausedLane(root string, n int) (*status.Lane, bool) {
	st, _ := status.Load(root)
	if st == nil {
		return nil, false
	}
	l := st.Get(n)
	if l == nil || l.PausedAt == "" || !l.Alive() {
		return nil, false
	}
	return l, true
}

// replacePaused ends lane n's paused round (as swim interrupt does) so a
// new one can start: it asks for the stop, lifts the pause and waits for
// the round's process to end (its outcome handlers run).
func replacePaused(root string, n int, l *status.Lane, newJob string) error {
	os.WriteFile(lane.ReplacedPath(root, n), []byte(newJob+"\n"), 0o644)
	if err := lane.RequestStop(root, n, l.Run); err != nil {
		return err
	}
	lane.ClearPause(root, n)
	history.Log(root, history.Entry{Event: history.StopReq, Lane: n, Job: l.Job, Run: l.Run, Detail: "paused, replaced by job " + or(newJob, "?")})
	budget := lane.DefaultHandlerTimeout
	if info, err := lane.ReadScript(root, n); err == nil && info.HandlerTimeout > 0 {
		budget = info.HandlerTimeout
	}
	deadline := time.Now().Add(budget + 30*time.Second)
	for time.Now().Before(deadline) {
		if _, ok := lane.Running(root, n); !ok {
			return nil
		}
		time.Sleep(pausePoll)
	}
	return errors.New("the paused round didn't end in time; swim interrupt " + strconv.Itoa(n) + " and try again")
}

// replaceIfPaused is swim run on a paused lane: the same job is resumed
// with swim unpause (or restarted with --replace); another job ends the
// paused round first, asked at a terminal (default no), or with --replace.
// Without either, nothing ends: a queued batch never replaces a paused
// round on its own.
func replaceIfPaused(root string, n int, rf runFlags) error {
	l, paused := pausedLane(root, n)
	if !paused {
		return nil
	}
	info, _ := lane.ReadScript(root, n)
	where := fmt.Sprintf("swim %d is paused in run %s (job %s", n, l.Run, short8(l.Job))
	if l.PausedAfter != "" {
		where += fmt.Sprintf(", after %q", l.PausedAfter)
	}
	where += ")"
	if !rf.replace {
		if info.Job != "" && info.Job == l.Job {
			return fmt.Errorf("swim %d is paused on this job: swim unpause %d resumes it, swim run %d --replace restarts it from the top", n, n, n)
		}
		if rf.yaml || rf.yamlOutput || !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
			return fmt.Errorf("%s; swim run %d --replace ends it (interrupted, its handlers run) and runs job %s", where, n, short8(info.Job))
		}
		fmt.Printf("%s.\n  %s now holds job %s (%q).\nEnd the paused round as interrupted and run job %s? [y/N] ", where, lane.ScriptName(root, n), short8(info.Job), info.Round, short8(info.Job))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("nothing run: the paused round goes on holding (swim unpause " + strconv.Itoa(n) + " resumes it)")
		}
	}
	if err := replacePaused(root, n, l, info.Job); err != nil {
		return err
	}
	fmt.Printf("swim %d: interrupted job %s (paused); starting %s\n", n, short8(l.Job), short8(info.Job))
	return nil
}
