package launcher

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// A lane waits on the nearest unapproved guard at or above it: a dry run
// there skips it. A lane with nothing unapproved above it waits on none.
func TestAwaitingApproval(t *testing.T) {
	deps := map[int][]int{2: {1}, 3: {2}, 5: {4}}
	guards := map[int][]GuardVerdict{
		1: {{Flag: "A"}},                 // not approved
		4: {{Flag: "B", Approved: true}}, // approved
	}
	got := awaitingApproval([]int{1, 2, 3, 4, 5, 6}, deps, guards)
	if want := map[int]int{1: 1, 2: 1, 3: 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("awaiting = %v, want %v", got, want)
	}
}

func TestApprovalsLine(t *testing.T) {
	pl := &plan{sel: []int{1, 2}, actions: map[int]planAction{1: actRun, 2: actRun},
		guards:   map[int][]GuardVerdict{1: {{Flag: "A"}, {Flag: "B", Approved: true}}},
		awaiting: map[int]int{1: 1, 2: 1}}
	got := approvalsLine(pl)
	for _, want := range []string{"1 of 2 guards approved", "Not yet: swim 1 A", "A dry run there skips swim 2 too."} {
		if !strings.Contains(got, want) {
			t.Errorf("approvals line %q missing %q", got, want)
		}
	}
	if approvalsLine(&plan{sel: []int{1}, actions: map[int]planAction{1: actRun}}) != "" {
		t.Error("a run with no guards has no approvals line")
	}
}

// A guard nothing can approve (its rule says evidence: true) says so in
// the plan, whatever would answer it.
func TestGuardVerdictNever(t *testing.T) {
	v := GuardVerdict{Flag: "A", Never: true, Why: "lookup.yml rule for A requires evidence-bound approvals"}
	if got := v.text(ui.Painter{}); got != "not approved: a dry run, whatever answers it; lookup.yml rule for A requires evidence-bound approvals" {
		t.Errorf("text %q", got)
	}
}
