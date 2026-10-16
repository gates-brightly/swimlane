package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/logparse"
)

// printedKept is how many of a lane's last output lines a round that
// never started keeps, for its reason.
const printedKept = 10

// NotStarted records a round of lane n that ended before it started (swim
// refused it, or its script exited before lane_init): a round in logPath
// (the lane's or the cell's log) whose STOP line says why, with the last
// lines the lane printed, and a fail entry in .swim.log. The caller
// records the status; why is its reason's text.
func NotStarted(root string, n int, logPath, job, round, run, why string, printed []string, exit int) {
	ts := time.Now().UTC().Format(time.RFC3339)
	if round == "" {
		round = "(no Round: line)"
	}
	if logparse.EnsureV2(logPath, n) == nil {
		os.MkdirAll(filepath.Dir(logPath), 0o755)
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			f.WriteString(logparse.NotStartedRound(ts, job, round, run, why, printed, exit))
			f.Close()
		}
	}
	history.Log(root, history.Entry{Event: history.Fail, Lane: n, Job: job, Run: run,
		Detail: fmt.Sprintf("exit=%d  not started: %s  %s", exit, why, round)})
}

// lastLine is the last line of printed with text, "" if none.
func lastLine(printed []string) string {
	for i := len(printed) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(printed[i]); t != "" {
			return t
		}
	}
	return ""
}
