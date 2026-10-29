package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// pendingHeader is what a pending round's lane script header declares
// (# Key: value), with each guard decided as swim run would decide it as
// it starts: swim status shows it under the lane, and --yaml as
// pending_header.
type pendingHeader struct {
	After  []string               `yaml:"after,flow,omitempty"`
	Guards []authority.GuardState `yaml:"guards,omitempty"`
	// Decisions are its # Decisions:, each with how swim run would answer
	// it now.
	Decisions []decisionState `yaml:"decisions,omitempty"`
	Timeout   string          `yaml:"timeout,omitempty"`
	Locks     []string        `yaml:"locks,flow,omitempty"`
	Includes  []string        `yaml:"includes,flow,omitempty"`
	Fail      string          `yaml:"fail,omitempty"`
	Targets   string          `yaml:"targets,omitempty"`
	Owner     string          `yaml:"owner,omitempty"`
}

// decisionState is one decision of a pending round, as swim run would
// answer it as it starts: NAME=N in this environment, or the prompt (a
// dry run when nobody can be asked).
type decisionState struct {
	Name  string `yaml:"name"`
	Desc  string `yaml:"description,omitempty"`
	Value string `yaml:"value,omitempty"` // NAME's value in this environment, if set
	Asks  bool   `yaml:"asks"`            // swim run would ask (approvals: prompt)
}

// Text says how it would be answered: "ROLLOUT=2 set", or that swim run
// asks.
func (d decisionState) Text() string {
	if d.Value != "" {
		return d.Name + "=" + d.Value + " set (swim run checks it's one of the choices)"
	}
	if d.Asks {
		return "not answered; swim run will ask (unanswered, the round ends SKIP)"
	}
	return "not answered; approvals: flags never asks: set " + d.Name + "=N"
}

// depDoc is where one dependency of a pending round stands (--yaml deps).
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
	now  time.Time
}

func newStatusView(root string, cfg *config.Config) *statusView {
	return &statusView{root: root, cfg: cfg, snap: authority.Take(root, os.Environ()), now: time.Now()}
}

// pending is lane l's pending round's header and state, or nil when it
// has none to show: no pending round, one that already passed, or a lane
// live in a run now (status shows the run instead).
func (v *statusView) pending(l *status.Lane) *pendingView {
	switch effectiveState(l) {
	case status.Running, status.Waiting, status.Queued, status.Locked:
		return nil
	}
	info, err := lane.ReadScript(v.root, l.Lane)
	if err != nil || !info.Pending() || launcher.AlreadyPassed(info, l) {
		return nil
	}
	h := pendingHeader{After: info.After, Timeout: info.TimeoutText, Locks: info.Locks, Includes: info.Includes, Targets: info.Target, Owner: info.Owner}
	if info.FailHard {
		h.Fail = "hard"
	}
	if len(info.Guards) > 0 {
		m, _ := manifest.Build(v.root, v.cfg, l.Lane, "")
		asks := v.cfg.ApprovalMode() != config.ApprovalsFlags
		for _, g := range info.Guards {
			a := authority.Ask{Root: v.root, Lane: l.Lane, Flag: g.Flag, Desc: g.Desc, Job: info.Job, Manifest: m}
			if m != nil {
				a.Script = m.ScriptSHA
			}
			h.Guards = append(h.Guards, v.snap.Guard(a, asks, v.now))
		}
	}
	for _, d := range info.Decisions {
		h.Decisions = append(h.Decisions, decisionState{Name: d.Flag, Desc: d.Desc, Value: v.snap.Env[d.Flag],
			Asks: v.cfg.ApprovalMode() != config.ApprovalsFlags})
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
		color := ui.Yellow
		switch {
		case g.Approved:
			color = ui.Green
		case g.Never:
			color = ui.Red
		}
		line("       "+g.Text(), color)
	}
	for _, d := range pv.Header.Decisions {
		head := "decision " + d.Name
		if d.Desc != "" {
			head += "  " + d.Desc
		}
		line(head, ui.Cyan)
		color := ui.Yellow
		if d.Value != "" {
			color = ui.Green
		}
		line("       "+d.Text(), color)
	}
	h := pv.Header
	var meta []string
	if h.Owner != "" {
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
