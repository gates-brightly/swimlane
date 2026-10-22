package logparse

import (
	"strconv"
	"strings"
)

// DocSchema identifies the YAML form of a round (swim log --yaml).
const DocSchema = "swim.log/v1"

// ResultDoc is one result line of a round, with its step's block.
type ResultDoc struct {
	Result   string   `yaml:"result" json:"result"` // PASS | FAIL | BLOCKED | SKIP | DRIFT | APPROVED | STOP | WARN
	Label    string   `yaml:"label" json:"label"`
	Detail   string   `yaml:"detail,omitempty" json:"detail,omitempty"`
	Stage    string   `yaml:"stage" json:"stage"`
	Handler  string   `yaml:"handler,omitempty" json:"handler,omitempty"` // the outcome handler it ran in
	Step     bool     `yaml:"step" json:"step"`                           // a step that ran a command (not a mark)
	Exit     *int     `yaml:"exit,omitempty" json:"exit,omitempty"`
	Duration *float64 `yaml:"duration_s,omitempty" json:"duration_s,omitempty"`
	Clock    string   `yaml:"clock,omitempty" json:"clock,omitempty"`
	Command  string   `yaml:"command,omitempty" json:"command,omitempty"`
	Output   []string `yaml:"output,omitempty" json:"output,omitempty"`
	Saved    string   `yaml:"saved,omitempty" json:"saved,omitempty"`
	Messages []string `yaml:"messages,omitempty" json:"messages,omitempty"` // status messages posted during the step (swim msg)
}

// Counts are a round's result totals.
type Counts struct {
	Pass  int `yaml:"pass" json:"pass"`
	Fail  int `yaml:"fail" json:"fail"`
	Skip  int `yaml:"skip" json:"skip"`
	Drift int `yaml:"drift" json:"drift"`
}

// Doc is one round as data.
type Doc struct {
	Schema    string            `yaml:"schema" json:"schema"`
	Lane      int               `yaml:"lane" json:"lane"`
	Job       string            `yaml:"job" json:"job"`
	Round     string            `yaml:"round" json:"round"`
	StartedAt string            `yaml:"started_at" json:"started_at"`
	Run       string            `yaml:"run,omitempty" json:"run,omitempty"`
	Context   map[string]string `yaml:"context,omitempty" json:"context,omitempty"`
	Finished  bool              `yaml:"finished" json:"finished"`
	Result    string            `yaml:"result" json:"result"` // passed | failed | interrupted | running (no END yet)
	Exit      *int              `yaml:"exit,omitempty" json:"exit,omitempty"`
	Duration  string            `yaml:"duration,omitempty" json:"duration,omitempty"`
	Counts    Counts            `yaml:"counts" json:"counts"`
	Stages    map[string]string `yaml:"stages" json:"stages"`
	Handlers  map[string]string `yaml:"handlers,omitempty" json:"handlers,omitempty"` // outcome handlers that ran: PASS | FAIL | SKIP | none
	Results   []ResultDoc       `yaml:"results" json:"results"`
	Failed    []string          `yaml:"failed_steps" json:"failed_steps"`
}

// detailExit is the exit code a step's detail names ("exit 2", "timeout:
// ..., exit 124"), ok false if none.
func detailExit(detail string) (int, bool) {
	for _, part := range strings.Split(detail, ", ") {
		if v, ok := strings.CutPrefix(part, "exit "); ok {
			if f := strings.Fields(v); len(f) > 0 {
				if n, err := strconv.Atoi(strings.TrimRight(f[0], ":;")); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// Doc renders the round as data for lane n.
func (r *Round) Doc(n int) Doc {
	d := Doc{Schema: DocSchema, Lane: n, Job: r.Job, Round: r.Title, StartedAt: r.StartedAt, Run: r.Run, Context: r.Context,
		Finished: r.Finished, Duration: r.Duration, Counts: Counts{r.Pass, r.Fail, r.Skip, r.Drift},
		Stages: map[string]string{}, Results: []ResultDoc{}, Failed: []string{}}
	switch {
	case !r.Finished:
		d.Result = "running"
	case r.Interrupt:
		d.Result = "interrupted"
	case r.ExitCode != 0:
		d.Result = "failed"
	default:
		d.Result = "passed"
	}
	if r.Finished {
		e := r.ExitCode
		d.Exit = &e
	}
	for _, st := range r.StageResults() {
		d.Stages[st.Name] = st.State
	}
	for _, h := range r.HandlerResults() {
		if d.Handlers == nil {
			d.Handlers = map[string]string{}
		}
		d.Handlers[h.Name] = h.State
	}
	for _, res := range r.Results {
		rd := ResultDoc{Result: res.Kind, Label: res.Label, Detail: res.Detail, Stage: res.Stage, Handler: res.Handler, Step: res.Step,
			Clock: res.Clock, Command: res.Cmd, Output: res.Output, Saved: res.Saved, Messages: res.Messages}
		if res.Step {
			dur := res.Dur
			rd.Duration = &dur
			e := 0
			if code, ok := detailExit(res.Detail); ok {
				e = code
			} else if res.Kind != Pass {
				e = -1
			}
			if e >= 0 || res.Kind == Pass {
				rd.Exit = &e
			}
		}
		d.Results = append(d.Results, rd)
	}
	for _, f := range r.Failed() {
		d.Failed = append(d.Failed, f.Text())
	}
	return d
}
