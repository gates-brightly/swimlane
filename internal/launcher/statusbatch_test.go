package launcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
)

// Skipped lanes queued together reach status.yml in one write, each with
// every update it was given; a flush writes what the window hasn't yet.
func TestStatusBatch(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".swim"), 0o755)
	b := newStatusBatch(root, 5)
	b.window = time.Second // long enough that "not yet" holds on a loaded machine
	for _, n := range []int{3, 1, 4} {
		n := n
		b.update(n, func(l *status.Lane) { l.State, l.Reason = status.Skipped, "swim 2 failed" })
		b.update(n, func(l *status.Lane) { l.Job = "job-" + string(rune('0'+n)) })
	}
	if st, _ := status.Load(root); st != nil && st.Get(3) != nil && st.Get(3).State == status.Skipped {
		t.Fatal("written before the window closed")
	}
	var st *status.File
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if st, _ = status.Load(root); st != nil && st.Get(4) != nil && st.Get(4).State == status.Skipped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the window closed, but nothing was written")
		}
	}
	for _, n := range []int{1, 3, 4} {
		if l := st.Get(n); l == nil || l.State != status.Skipped || l.Reason != "swim 2 failed" || l.Job != "job-"+string(rune('0'+n)) {
			t.Errorf("swim %d: %+v", n, l)
		}
	}
	b.update(5, func(l *status.Lane) { l.State = status.Skipped })
	b.flush()
	st, _ = status.Load(root)
	if l := st.Get(5); l == nil || l.State != status.Skipped {
		t.Errorf("flush didn't write swim 5: %+v", l)
	}
}
