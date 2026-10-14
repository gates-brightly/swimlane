package status

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The lane state machine (docs/state.md), as the rules every write of
// status.yml is checked against. A lane or cell at rest (idle or finished)
// may start a new round, or be archived, refused or skipped, from anywhere;
// the rules are about the round in flight:
//
//   - a running round ends only as passed, failed or interrupted;
//   - a lane waiting for its turn (waiting, locked, queued) moves on, or
//     ends, but isn't put back to idle;
//   - a finished round's result never changes in place: passed, failed,
//     skipped and interrupted turn into one another only as a new round
//     (another start time or run). A failure never becomes a pass.
//
// Only swim status --rebuild, which reconstructs every entry from the logs,
// writes without the check (Rebuild).

// CheckEnv makes an illegal transition panic (the tests set it, and test
// binaries always check). Elsewhere status.yml is the last known state,
// and a write the rules don't expect still goes through rather than leave
// a lane stuck.
const CheckEnv = "SWIM_CHECK_STATES"

func finished(s string) bool {
	return s == Passed || s == Failed || s == Skipped || s == Interrupted
}

func waitingTurn(s string) bool { return s == Waiting || s == Locked || s == Queued }

// Transition checks one lane's (or cell's) move from its entry before a
// write to its entry after it.
func Transition(from, to *Lane) error {
	a, b := from.State, to.State
	switch {
	case a == b || a == "":
		return nil
	case a == Running && !finished(b) || a == Running && b == Skipped:
		return fmt.Errorf("a running round can only end (passed, failed, interrupted), not go %s", b)
	case waitingTurn(a) && b == Idle:
		return fmt.Errorf("a lane %s for its turn can't go back to idle", a)
	case finished(a) && finished(b) && sameRound(from, to):
		return fmt.Errorf("a %s round can't become %s without a new round", a, b)
	}
	return nil
}

// sameRound reports whether two entries record one round: the same start
// time and run.
func sameRound(a, b *Lane) bool {
	return ptrEq(a.StartedAt, b.StartedAt) && a.Run == b.Run
}

func ptrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// checkTransitions compares every lane and cell of before and after.
func checkTransitions(before, after *File) []string {
	old := map[string]*Lane{}
	for i := range before.Lanes {
		l := &before.Lanes[i]
		old[fmt.Sprint(l.Lane)] = l
		for j := range l.Cells {
			old[fmt.Sprintf("%d %s", l.Lane, l.Cells[j].Cell)] = &l.Cells[j]
		}
	}
	var bad []string
	check := func(key, who string, to *Lane) {
		if from := old[key]; from != nil {
			if err := Transition(from, to); err != nil {
				bad = append(bad, fmt.Sprintf("%s: %s -> %s: %v", who, from.State, to.State, err))
			}
		}
	}
	for i := range after.Lanes {
		l := &after.Lanes[i]
		check(fmt.Sprint(l.Lane), fmt.Sprintf("swim %d", l.Lane), l)
		for j := range l.Cells {
			c := &l.Cells[j]
			check(fmt.Sprintf("%d %s", l.Lane, c.Cell), fmt.Sprintf("swim %d (%s)", l.Lane, c.Cell), c)
		}
	}
	return bad
}

// enforce panics on illegal transitions where they are checked.
func enforce(bad []string) {
	if len(bad) == 0 || !(testing.Testing() || os.Getenv(CheckEnv) == "1") {
		return
	}
	panic("status.yml: illegal state transition (docs/state.md): " + strings.Join(bad, "; "))
}

// clone copies what the transition check reads (each lane's and cell's
// state, start time and run), so fn's changes don't reach it.
func (f *File) clone() *File {
	c := &File{Lanes: make([]Lane, len(f.Lanes))}
	cp := func(l Lane) Lane {
		out := Lane{Lane: l.Lane, Cell: l.Cell, State: l.State, Run: l.Run}
		if l.StartedAt != nil {
			out.StartedAt = Str(*l.StartedAt)
		}
		return out
	}
	for i, l := range f.Lanes {
		c.Lanes[i] = cp(l)
		for _, cell := range l.Cells {
			c.Lanes[i].Cells = append(c.Lanes[i].Cells, cp(cell))
		}
	}
	return c
}
