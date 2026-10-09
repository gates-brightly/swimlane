package launcher

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestEventStream(t *testing.T) {
	var b bytes.Buffer
	ev := newEvents(&b, true)
	line := "hello: world"
	ev.emit(Event{Event: "run", Run: "r-x", Lanes: []int{1, 2}, At: "t"})
	ev.emit(Event{Event: "waiting", Lane: 2, On: []int{1}})
	ev.emit(Event{Event: "queued", Lane: 2, Ahead: intp(0)})
	ev.emit(Event{Event: "locked", Lane: 2, Lock: "db: swim 1"})
	ev.emit(Event{Event: "start", Lane: 1, Job: "j1", Round: "r"})
	ev.emit(Event{Event: "output", Lane: 1, Line: &line})
	ev.emit(Event{Event: "step", Lane: 1, Label: "a", Result: "PASS", Exit: intp(0), Duration: floatp(0)})
	ev.emit(Event{Event: "finish", Lane: 2, Result: "skipped", Reason: "swim 1 failed"})
	outcomes := map[int]*Outcome{
		1: {N: 1, Job: "j1", State: "failed", Exit: 1, Start: time.Unix(0, 0), End: time.Unix(1, 500e6)},
		2: {N: 2, State: "skipped", Exit: -1, Reason: "swim 1 failed"},
	}
	ev.failed[1] = []string{"FAIL  a (exit 1)"}
	ev.summary(Options{Root: "/r", RunID: "r-x"}, []int{1, 2}, outcomes, 1, false, 2*time.Second)

	dec := yaml.NewDecoder(strings.NewReader(b.String()))
	var kinds []string
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%v\n%s", err, b.String())
		}
		if doc["schema"] != RunSchema {
			t.Fatalf("document without schema: %v", doc)
		}
		kinds = append(kinds, doc["event"].(string))
		switch doc["event"] {
		case "queued":
			if doc["ahead"] != 0 {
				t.Errorf("queued keeps ahead: 0: %v", doc)
			}
		case "output":
			if doc["line"] != line {
				t.Errorf("output line: %v", doc)
			}
		case "summary":
			lanes := doc["lanes"].([]any)
			l1 := lanes[0].(map[string]any)
			if doc["result"] != "failed" || doc["exit"] != 1 || l1["duration_s"] != 1.5 || l1["failed_steps"].([]any)[0] != "FAIL  a (exit 1)" {
				t.Errorf("summary: %v", doc)
			}
			if l2 := lanes[1].(map[string]any); l2["exit"] != nil || l2["reason"] != "swim 1 failed" {
				t.Errorf("skipped lane: %v", l2)
			}
		}
	}
	if got := strings.Join(kinds, " "); got != "run waiting queued locked start output step finish summary" {
		t.Fatalf("events: %s", got)
	}
	var nilEv *events
	nilEv.emit(Event{Event: "run"}) // a nil stream writes nothing
}
