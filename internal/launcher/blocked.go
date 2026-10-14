package launcher

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/status"
)

// BlockedLanes scans each selected lane script (comments excluded) for
// blocked commands and returns, per refused lane, why: e.g.
// `blocked: lane.3.sh:41 matches "git push"`.
func BlockedLanes(root string, cfg *config.Config, sel []int) map[int]string {
	out := map[int]string{}
	for _, n := range sel {
		if why := blockedIn(root, cfg, n, lane.Script(root, n)); why != "" {
			out[n] = why
		}
	}
	return out
}

// blockedIn is why lane n's script at path (the working tree's, or its
// staged copy) is refused for a blocked command; "" if it isn't.
func blockedIn(root string, cfg *config.Config, n int, path string) string {
	data, err := os.ReadFile(path)
	if err != nil || lane.IsBinary(data) {
		return ""
	}
	hits := policy.ScanScript(string(data), cfg.Blocked())
	if len(hits) == 0 {
		return ""
	}
	var parts []string
	for _, h := range hits {
		parts = append(parts, fmt.Sprintf("%s:%d matches %q", lane.ScriptName(root, n), h.Line, h.Pattern))
	}
	return "blocked: " + strings.Join(parts, ", ") + " (swim never pushes, commits or pulls)"
}

// refuseBlocked fails a lane before it starts: nothing in it runs, and its
// dependents are skipped like any failure's.
func refuseBlocked(o Options, n int, reason string, disp *display.Display) *Outcome {
	info, _ := lane.ReadScript(o.Root, n)
	disp.Set(n, func(v *display.LaneView) {
		v.State, v.Exit, v.Reason = display.Failed, policy.ExitBlocked, reason
	})
	disp.Line(n, reason)
	line := logparse.ResultLine(logparse.Blocked, info.Round, strings.TrimPrefix(reason, "blocked: "))
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.ResetRun()
		l.State, l.Reason = status.Failed, reason
		l.Round, l.Job, l.Run = info.Round, info.Job, o.RunID
		l.ExitCode = status.Int(policy.ExitBlocked)
		l.FailedSteps = []string{line}
		l.FinishedAt = status.Str(status.Now())
	})
	history.Log(o.Root, history.Entry{Event: history.Fail, Lane: n, Job: info.Job, Run: o.RunID, Detail: reason + "  " + info.Round})
	return &Outcome{N: n, Job: info.Job, Round: info.Round, State: status.Failed, Exit: policy.ExitBlocked, Reason: reason, Blocked: true, End: time.Now()}
}

// refuseStart fails a lane whose script swim can't start (no runner for its
// extension, a binary that isn't executable): nothing runs, the reason is
// recorded in status, and its dependents are skipped like any failure's.
func refuseStart(o Options, n int, err error, disp *display.Display) *Outcome {
	info, _ := lane.ReadScript(o.Root, n)
	lines := strings.Split(err.Error(), "\n")
	reason := "cannot start: " + lines[0]
	disp.Set(n, func(v *display.LaneView) {
		v.State, v.Exit, v.Reason, v.Round, v.Job = display.Failed, 127, reason, info.Round, info.Job
	})
	for _, l := range lines {
		disp.Line(n, l)
	}
	status.Update(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.ResetRun()
		l.State, l.Reason = status.Failed, reason
		l.Round, l.Job, l.Run = info.Round, info.Job, o.RunID
		l.ExitCode = status.Int(127)
		l.FailedSteps = []string{reason}
		l.FinishedAt = status.Str(status.Now())
	})
	history.Log(o.Root, history.Entry{Event: history.Fail, Lane: n, Job: info.Job, Run: o.RunID, Detail: reason + "  " + info.Round})
	return &Outcome{N: n, Job: info.Job, Round: info.Round, State: status.Failed, Exit: 127, Reason: reason, Blocked: true, End: time.Now()}
}

// uncheckable says why lane n can't be checked read-only ("" if it can): a
// compiled lane (swim can't see its stages), or a script with no stage
// calls (nowhere safe to stop).
func uncheckable(root string, n int) string {
	data, err := os.ReadFile(lane.Script(root, n))
	if err != nil {
		return ""
	}
	if lane.IsBinary(data) {
		return "cannot check: a compiled lane (swim can't see its stages)"
	}
	if info := lane.ParseScript(string(data)); !info.Staged {
		return "cannot check: no stages (nowhere safe to stop before a change)"
	}
	return ""
}

// refuseCheck records a lane a check won't run (it shows in the plan).
func refuseCheck(o Options, n int, why string, disp *display.Display) *Outcome {
	info, _ := lane.ReadScript(o.Root, n)
	disp.Set(n, func(v *display.LaneView) { v.State, v.Reason = display.Skipped, why })
	disp.Line(n, why)
	status.UpdateCheck(o.Root, n, o.Cfg.Lanes, func(l *status.Lane) {
		l.CheckResult, l.CheckAt, l.CheckStop, l.CheckGuards = "refused", status.Now(), why, nil
		l.CheckSHA = ""
	})
	return &Outcome{N: n, Job: info.Job, Round: info.Round, State: status.Skipped, Exit: -1, Reason: why, End: time.Now()}
}
