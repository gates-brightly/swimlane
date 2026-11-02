package display

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// laneTag is an output line's lane prefix: "[3] ", or a job's "[3.1] ".
var laneTag = regexp.MustCompile(`^\[(\d+)((?:\.[0-9A-Za-z-]+)*)\] `)

// StyleOutputLine colours a plain line of lane output the way swim run's
// live view shows it, for the views that read it back as text (swim
// watch's follow views): its [N] in the lane's colour, a step's ==>
// header and a -- stage header in the lane's colour, its $ command dim,
// result kinds (PASS, FAIL, ...) in their colours, and lane log lines as
// swim log colours them. A line already coloured is left as it is; any
// other escape sequence is removed first.
func StyleOutputLine(l string) string {
	l = ui.SafeKeepSGR(l)
	if strings.Contains(l, "\x1b") {
		return l
	}
	p := ui.Painter{On: true}
	n, tag := 0, ""
	if m := laneTag.FindStringSubmatch(l); m != nil {
		n, _ = strconv.Atoi(m[1])
		job := m[1] + m[2]
		tag = JobPrefix(n, job, true)
		l = l[len(m[0]):]
	}
	body := strings.TrimLeft(l, " ")
	indent := l[:len(l)-len(body)]
	switch {
	case strings.HasPrefix(body, "==> "), strings.HasPrefix(body, logparse.StageMark):
		return tag + indent + p.Paint(ui.LaneColor(n)+ui.Bold, body)
	case strings.HasPrefix(body, "$ "):
		return tag + indent + p.Paint(ui.Dim, body)
	}
	if indent == "" {
		// swim run's own result lines ("PASS  build  0.4s") have no indent;
		// a log's have two.
		if kind, _, ok := logparse.SplitResult("  "+body, stepRunning); ok {
			return tag + p.Paint(ui.StateColor(kind)+ui.Bold, kind) + body[len(kind):]
		}
	}
	return tag + ColorLaneLogLine(l, n, true)
}

// stepRunning is a running step's result kind on swim log's view of its
// spool (step.Running; display can't import step).
const stepRunning = "RUNNING"
