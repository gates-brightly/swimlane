package launcher

import (
	"fmt"
	"os"
	"strings"

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
	patterns := cfg.Blocked()
	for _, n := range sel {
		data, err := os.ReadFile(lane.Script(root, n))
		if err != nil {
			continue
		}
		hits := policy.ScanScript(string(data), patterns)
		if len(hits) == 0 {
			continue
		}
		var parts []string
		for _, h := range hits {
			parts = append(parts, fmt.Sprintf("lane.%d.sh:%d matches %q", n, h.Line, h.Pattern))
		}
		out[n] = "blocked: " + strings.Join(parts, ", ") + " (swim never pushes, commits or pulls)"
	}
	return out
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
	return &Outcome{N: n, Job: info.Job, State: status.Failed, Exit: policy.ExitBlocked, Reason: reason, Blocked: true}
}
