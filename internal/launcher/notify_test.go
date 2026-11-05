package launcher

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/notify"
	"github.com/gates-brightly/swimlane/internal/status"
)

// The run's bar counts its lanes that ended (not lanes it leaves out): red
// once one failed, paused while one waits on the operator, which comes
// first.
func TestProgressOf(t *testing.T) {
	v := func(n int, state, approval string) display.LaneView {
		return display.LaneView{N: n, State: state, Approval: approval}
	}
	sel := []int{1, 2, 3, 4}
	for _, c := range []struct {
		views []display.LaneView
		want  notify.Progress
	}{
		{[]display.LaneView{v(1, display.Running, ""), v(2, display.Waiting, "")}, notify.Progress{State: notify.Indeterminate}},
		{[]display.LaneView{v(1, display.Passed, ""), v(2, display.Running, ""), v(9, display.Passed, "")}, notify.Progress{State: notify.Normal, Percent: 25}},
		{[]display.LaneView{v(1, display.Passed, ""), v(2, display.Failed, "")}, notify.Progress{State: notify.Error, Percent: 50}},
		{[]display.LaneView{v(1, display.Failed, ""), v(2, display.Running, "deploy")}, notify.Progress{State: notify.Paused, Percent: 25}},
		{[]display.LaneView{v(1, display.Passed, ""), v(2, display.Skipped, ""), v(3, display.Done, ""), v(4, display.Interrupted, "")}, notify.Progress{State: notify.Error, Percent: 100}},
	} {
		if got := progressOf(c.views, sel); got != c.want {
			t.Errorf("%+v: got %+v, want %+v", c.views, got, c.want)
		}
	}
}

// A run's notifications start with where they came from: the repo, and
// the branch when it isn't the default.
func TestNotifyTitles(t *testing.T) {
	var b bytes.Buffer
	o := Options{Notify: notify.New(&b), Origin: notify.Origin{Repo: "infra", Branch: "fix-dns", Default: "main"}}
	notifyAsk(o, approval.Request{Lane: 2, Flag: "DEPLOY", Desc: "deploy 2.4.1"})
	if got, want := b.String(), "\x1b]777;notify;infra@fix-dns · lane 2 needs you;approval of DEPLOY: deploy 2.4.1\x07"; got != want {
		t.Errorf("ask: got %q, want %q", got, want)
	}
	b.Reset()
	o.Origin.Branch = "main"
	notifyEnd(o, []int{1, 2}, map[string]int{status.Passed: 1, status.Failed: 1}, 1, false, 3*time.Second)
	if got, want := b.String(), "\x1b]777;notify;infra · run failed;"; !strings.HasPrefix(got, want) {
		t.Errorf("end: got %q, want it to start %q", got, want)
	}
}
