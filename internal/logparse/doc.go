package logparse

import (
	"regexp"
	"strconv"
)

// DocSchema identifies the YAML form of a round (swim log --yaml).
const DocSchema = "swim.log/v1"

// ResultDoc is one result line of a round, with its step's block.
type ResultDoc struct {
	Result   string   `yaml:"result"` // PASS | FAIL | BLOCKED | SKIP | DRIFT | APPROVED | STOP | WARN
	Label    string   `yaml:"label"`
	Detail   string   `yaml:"detail,omitempty"`
	Stage    string   `yaml:"stage"`
	Step     bool     `yaml:"step"` // a step that ran a command (not a mark)
	Exit     *int     `yaml:"exit,omitempty"`
	Duration *float64 `yaml:"duration_s,omitempty"`
	Clock    string   `yaml:"clock,omitempty"`
	Command  string   `yaml:"command,omitempty"`
	Output   []string `yaml:"output,omitempty"`
	Saved    string   `yaml:"saved,omitempty"`
}

// Counts are a round's result totals.
type Counts struct {
	Pass  int `yaml:"pass"`
	Fail  int `yaml:"fail"`
	Skip  int `yaml:"skip"`
	Drift int `yaml:"drift"`
}

// Doc is one round as data.
type Doc struct {
	Schema    string            `yaml:"schema"`
	Lane      int               `yaml:"lane"`
	Job       string            `yaml:"job"`
	Round     string            `yaml:"round"`
	StartedAt string            `yaml:"started_at"`
	Run       string            `yaml:"run,omitempty"`
	Context   map[string]string `yaml:"context,omitempty"`
	Finished  bool              `yaml:"finished"`
	Result    string            `yaml:"result"` // passed | failed | interrupted | running (no END yet)
	Exit      *int              `yaml:"exit,omitempty"`
	Duration  string            `yaml:"duration,omitempty"`
	Counts    Counts            `yaml:"counts"`
	Stages    map[string]string `yaml:"stages"`
	Results   []ResultDoc       `yaml:"results"`
	Failed    []string          `yaml:"failed_steps"`
}

var exitDetailRE = regexp.MustCompile(`(?:^|, )exit (-?\d+)`)

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
	for _, res := range r.Results {
		rd := ResultDoc{Result: res.Kind, Label: res.Label, Detail: res.Detail, Stage: res.Stage, Step: res.Step,
			Clock: res.Clock, Command: res.Cmd, Output: res.Output, Saved: res.Saved}
		if res.Step {
			dur := res.Dur
			rd.Duration = &dur
			e := 0
			if m := exitDetailRE.FindStringSubmatch(res.Detail); m != nil {
				e, _ = strconv.Atoi(m[1])
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
