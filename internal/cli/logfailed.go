package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/logparse"
)

// failedKinds are the results swim log --failed shows: the round's steps
// that failed, were blocked or drifted, and why it stopped.
var failedKinds = map[string]bool{logparse.Fail: true, logparse.Blocked: true, logparse.Drift: true, logparse.Stop: true}

// resultLineRE is a result line ("  KIND  <label> ..."): its kind and the
// rest.
var resultLineRE = regexp.MustCompile(`^  ([A-Z]+)  (.*)$`)

// failedView narrows a round (its log lines) to what a fix needs (swim log
// --failed): the round's header, and for each step that failed, stopped or
// drifted, the stage it was in, one line for the step before it, and its
// result with its command and output in full. Passing steps are counted,
// not shown, and the END lines close it. ok is false when nothing failed.
func failedView(round []string) (out []string, ok bool) {
	stage, shownStage := "", ""
	prev, passed := "", 0
	inEnd, header, show := false, false, false
	for i, l := range round {
		if header && !strings.HasPrefix(l, "   ") {
			header = false
		}
		switch {
		case i == 0:
			out = append(out, l)
			header = true // its context lines follow
			continue
		case header:
			out = append(out, l)
			continue
		case strings.HasPrefix(l, "== END") || strings.HasPrefix(l, "=== SUMMARY"):
			inEnd = true
			if passed > 0 {
				out = append(out, "   ("+plural(passed, "passed step")+" not shown)")
			}
			out = append(out, l)
			continue
		case inEnd:
			out = append(out, l)
			continue
		case strings.HasPrefix(l, "-- stage "):
			stage, show = l, false
			continue
		case strings.HasPrefix(l, "        "):
			if show {
				out = append(out, l)
			}
			continue
		}
		m := resultLineRE.FindStringSubmatch(l)
		if m == nil {
			show = false
			continue
		}
		show = failedKinds[m[1]]
		if show {
			ok = true
			if stage != "" && stage != shownStage {
				out = append(out, stage)
				shownStage = stage
			}
			if prev != "" {
				out = append(out, "   before it: "+prev)
			}
			out = append(out, l)
		} else if m[1] == logparse.Pass {
			passed++
		}
		// Its label and detail, without the duration and clock columns.
		what, _, _ := strings.Cut(m[2], "  ")
		prev = strings.TrimSpace(what) + " (" + m[1] + ")"
	}
	return out, ok
}

// failedOnly narrows a round's document to its failed results (swim log
// --failed --yaml), each with the step before it.
func failedOnly(d logparse.Doc) logparse.Doc {
	var keep []logparse.ResultDoc
	prev := ""
	for _, r := range d.Results {
		if failedKinds[r.Result] {
			r.Before = prev
			keep = append(keep, r)
		}
		prev = r.Label
	}
	d.Results = keep
	if d.Results == nil {
		d.Results = []logparse.ResultDoc{}
	}
	return d
}

// failed prints each of lane n's rounds as failedView narrows it, or says
// it has no failed steps.
func (lw logWriter) failed(n int, rounds [][]string) error {
	for _, r := range rounds {
		view, ok := failedView(r)
		if !ok {
			title := ""
			if pr, err := logparse.ParseRound(r); err == nil && pr.Title != "" {
				title = ": " + pr.Title
			}
			fmt.Fprintf(lw.w, "no failed steps (lane %d%s)\n", n, title)
			continue
		}
		lw.lines(view)
	}
	return nil
}
