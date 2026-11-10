package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// pendingHeader is what a ready round's lane script header declares
// (# Key: value), with each guard decided as swim run would decide it as
// it starts: swim status shows it under the lane, and --yaml as
// pending_header.
type pendingHeader struct {
	After    []string               `yaml:"after,flow,omitempty"`
	Guards   []authority.GuardState `yaml:"guards,omitempty"`
	Timeout  string                 `yaml:"timeout,omitempty"`
	Locks    []string               `yaml:"locks,flow,omitempty"`
	Includes []string               `yaml:"includes,flow,omitempty"`
	Fail     string                 `yaml:"fail,omitempty"`
	Targets  string                 `yaml:"targets,omitempty"`
	Owner    string                 `yaml:"owner,omitempty"`
}

// depDoc is where one dependency of a ready round stands (--yaml deps).
type depDoc struct {
	Lane  int    `yaml:"lane"`
	Job   string `yaml:"job,omitempty"`
	State string `yaml:"state"`
	Note  string `yaml:"note,omitempty"`
	Error string `yaml:"error,omitempty"`
}

// pendingView is a pending lane's header and where it stands.
type pendingView struct {
	Header pendingHeader
	Deps   []depDoc
	// DepsErr: its After: doesn't resolve, or makes a cycle (swim run
	// refuses it before anything starts).
	DepsErr string
}

// statusView decides pending lanes' guards from one snapshot, as swim run
// takes one as it starts.
type statusView struct {
	root string
	cfg  *config.Config
	snap *authority.Snapshot
}

func newStatusView(root string, cfg *config.Config) *statusView {
	return &statusView{root: root, cfg: cfg, snap: authority.Take(os.Environ())}
}

// pending is lane l's ready round's header and state, or nil when it
// has none to show: no ready round, one that already passed, or a lane
// live in a run now (status shows the run instead).
func (v *statusView) pending(l *status.Lane) *pendingView {
	switch effectiveState(l) {
	case status.Running, status.Waiting, status.Queued:
		return nil
	}
	info, err := lane.ReadScript(v.root, l.Lane)
	if err != nil || !info.Pending() || launcher.AlreadyPassed(info, l) {
		return nil
	}
	h := pendingHeader{After: info.After, Timeout: info.TimeoutText, Locks: info.Locks, Includes: info.Includes, Targets: info.Target, Owner: info.Owner}
	if t := strings.ToLower(info.TimeoutText); info.Timeout == 0 && t != "" && t != "-" && t != "none" {
		// Not a duration: no limit would apply, and swim lint refuses it.
		h.Timeout = fmt.Sprintf("%q unreadable, none applies (swim lint)", info.TimeoutText)
	}
	if info.FailHard {
		h.Fail = "hard"
	}
	if len(info.Guards) > 0 {
		for _, g := range info.Guards {
			gs := v.snap.Guard(g.Flag, g.Desc)
			gs.Why = g.Why
			h.Guards = append(h.Guards, gs)
		}
	}
	pv := &pendingView{Header: h}
	deps, err := launcher.DepStates(v.root, v.cfg, l.Lane)
	if err != nil {
		pv.DepsErr = err.Error()
	}
	for _, d := range deps {
		pv.Deps = append(pv.Deps, depDoc{Lane: d.Lane, Job: d.Job, State: d.State, Note: d.Note})
	}
	return pv
}

// print writes the header's lines under the lane's row: dependencies,
// guards (each with how swim run would decide it), then the rest on one
// line. Only what the header sets.
func (pv *pendingView) print(p ui.Painter, indent string) {
	line := func(s, color string) { fmt.Println(indent + p.Paint(color, s)) }
	for _, d := range pv.Deps {
		what := launcher.Dep{Lane: d.Lane, Job: d.Job}.String()
		if d.State == "passed" {
			line("after "+what+" (passed)", ui.Dim)
			continue
		}
		line(fmt.Sprintf("after %s (%s: %s)", what, d.State, d.Note), ui.Yellow)
	}
	if pv.DepsErr != "" {
		line("after: "+pv.DepsErr, ui.Red)
	}
	for _, g := range pv.Header.Guards {
		head := "guard " + g.Flag
		if g.Desc != "" {
			head += "  " + g.Desc
		}
		line(head, ui.Cyan)
		if g.Why != "" {
			line("       why: "+g.Why, ui.Dim)
		}
		color := ui.Yellow
		if g.Approved {
			color = ui.Green
		}
		line("       "+g.Text(), color)
	}
	h := pv.Header
	var meta []string
	// Your own name is no news: the owner shows when it's someone else.
	if h.Owner != "" && h.Owner != lane.ClaimOwner() && h.Owner != os.Getenv("USER") {
		meta = append(meta, "owner "+h.Owner)
	}
	if h.Timeout != "" {
		meta = append(meta, "timeout "+h.Timeout)
	}
	if len(h.Locks) > 0 {
		meta = append(meta, "locks "+strings.Join(h.Locks, ", "))
	}
	if len(h.Includes) > 0 {
		meta = append(meta, "includes "+strings.Join(h.Includes, ", "))
	}
	if h.Fail != "" {
		meta = append(meta, "fail "+h.Fail)
	}
	if h.Targets != "" {
		meta = append(meta, "targets "+h.Targets)
	}
	if len(meta) > 0 {
		line(strings.Join(meta, " · "), ui.Dim)
	}
}
