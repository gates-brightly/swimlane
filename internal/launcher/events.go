package launcher

import (
	"bytes"
	"io"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
)

// RunSchema identifies swim run --yaml documents.
const RunSchema = "swim.run/v1"

// Event is one document of swim run --yaml. Fields not used by an event
// type are left out.
type Event struct {
	Schema      string           `yaml:"schema"`
	Event       string           `yaml:"event"` // run | waiting | locked | queued | start | output | step | finish | summary
	Run         string           `yaml:"run,omitempty"`
	Lane        int              `yaml:"lane,omitempty"`
	Job         string           `yaml:"job,omitempty"`
	Round       string           `yaml:"round,omitempty"`
	Lanes       []int            `yaml:"lanes,flow,omitempty"`
	MaxParallel int              `yaml:"max_parallel,omitempty"`
	On          []int            `yaml:"on,flow,omitempty"`
	Lock        string           `yaml:"lock,omitempty"`
	Ahead       *int             `yaml:"ahead,omitempty"`
	Line        *string          `yaml:"line,omitempty"`
	Label       string           `yaml:"label,omitempty"`
	Stage       string           `yaml:"stage,omitempty"`
	Result      string           `yaml:"result,omitempty"`
	Detail      string           `yaml:"detail,omitempty"`
	Exit        *int             `yaml:"exit,omitempty"`
	Reason      string           `yaml:"reason,omitempty"`
	Counts      *logparse.Counts `yaml:"counts,omitempty"`
	FailedSteps []string         `yaml:"failed_steps,omitempty"`
	Duration    *float64         `yaml:"duration_s,omitempty"`
	Note        string           `yaml:"note,omitempty"`
	At          string           `yaml:"at,omitempty"`
}

// Summary is the stream's last document (event: summary).
type Summary struct {
	Schema   string         `yaml:"schema"`
	Event    string         `yaml:"event"`
	Run      string         `yaml:"run"`
	Result   string         `yaml:"result"` // passed | failed | interrupted | nothing
	Exit     int            `yaml:"exit"`
	Duration float64        `yaml:"duration_s"`
	Lanes    []LaneResult   `yaml:"lanes"`
	Logs     map[int]string `yaml:"logs"`
	Note     string         `yaml:"note,omitempty"`
	At       string         `yaml:"at"`
}

// LaneResult is one lane's line in the summary event.
type LaneResult struct {
	Lane        int      `yaml:"lane"`
	Job         string   `yaml:"job,omitempty"`
	Result      string   `yaml:"result"` // passed | failed | skipped | interrupted
	Exit        *int     `yaml:"exit,omitempty"`
	Reason      string   `yaml:"reason,omitempty"`
	Duration    float64  `yaml:"duration_s"`
	FailedSteps []string `yaml:"failed_steps,flow,omitempty"`
}

// events writes the YAML event stream; a nil *events writes nothing, so the
// launcher calls it unconditionally.
type events struct {
	mu     sync.Mutex
	w      io.Writer
	output bool // also emit lane output lines
	n      int
	failed map[int][]string
}

func newEvents(w io.Writer, output bool) *events {
	if w == nil {
		return nil
	}
	return &events{w: w, output: output, failed: map[int][]string{}}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func intp(i int) *int                { return &i }
func floatp(f float64) *float64      { return &f }
func round2(d time.Duration) float64 { return float64(d.Milliseconds()/10) / 100 }

// emit writes one document, flushed at once so a reader can stream.
func (e *events) emit(ev Event) {
	if e == nil {
		return
	}
	ev.Schema = RunSchema
	e.write(ev)
}

func (e *events) write(doc any) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if enc.Encode(doc) != nil || enc.Close() != nil {
		return
	}
	data := b.Bytes()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.n > 0 {
		io.WriteString(e.w, "---\n")
	}
	e.n++
	e.w.Write(data)
}

func (e *events) wantsOutput() bool { return e != nil && e.output }

// laneDone emits a lane's step results (from its log's round in this run)
// and then its finish event.
func (e *events) laneDone(o Options, oc *Outcome) {
	if e == nil {
		return
	}
	ev := Event{Event: "finish", Lane: oc.N, Job: oc.Job, Result: oc.State, Reason: oc.Reason, At: now()}
	if oc.Exit >= 0 {
		ev.Exit = intp(oc.Exit)
	}
	if !oc.Start.IsZero() {
		ev.Duration = floatp(round2(oc.End.Sub(oc.Start)))
		if r, err := logparse.ParseFile(lane.Log(o.Root, oc.N)); err == nil && r.Found && r.Run == o.RunID {
			for _, res := range r.Doc(oc.N).Results {
				e.emit(Event{Event: "step", Lane: oc.N, Label: res.Label, Stage: res.Stage, Result: res.Result,
					Exit: res.Exit, Duration: res.Duration, Detail: res.Detail})
			}
			ev.Counts = &logparse.Counts{Pass: r.Pass, Fail: r.Fail, Skip: r.Skip, Drift: r.Drift}
			ev.FailedSteps = []string{}
			for _, f := range r.Failed() {
				ev.FailedSteps = append(ev.FailedSteps, f.Text())
			}
		}
	}
	e.mu.Lock()
	e.failed[oc.N] = ev.FailedSteps
	e.mu.Unlock()
	e.emit(ev)
}

// summary is the stream's last document.
func (e *events) summary(o Options, sel []int, outcomes map[int]*Outcome, code int, interrupted bool, elapsed time.Duration) {
	if e == nil {
		return
	}
	result := status.Passed
	if code != 0 {
		result = status.Failed
	}
	if interrupted {
		result = status.Interrupted
	}
	ev := Summary{Schema: RunSchema, Event: "summary", Run: o.RunID, Result: result, Exit: code, Duration: round2(elapsed),
		Lanes: []LaneResult{}, Logs: map[int]string{}, At: now()}
	for _, n := range sel {
		oc := outcomes[n]
		lr := LaneResult{Lane: n, Job: oc.Job, Result: oc.State, Reason: oc.Reason, FailedSteps: e.failed[n]}
		if oc.Exit >= 0 {
			lr.Exit = intp(oc.Exit)
		}
		if !oc.Start.IsZero() {
			lr.Duration = round2(oc.End.Sub(oc.Start))
			ev.Logs[n] = rel(o.Root, lane.Log(o.Root, n))
		}
		ev.Lanes = append(ev.Lanes, lr)
	}
	e.write(ev)
}

// nothing is the summary of a run with no lanes to run.
func (e *events) nothing(o Options, note string) {
	if e == nil {
		return
	}
	e.write(Summary{Schema: RunSchema, Event: "summary", Run: o.RunID, Result: "nothing", Lanes: []LaneResult{},
		Logs: map[int]string{}, Note: note, At: now()})
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
