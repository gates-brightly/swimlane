package launcher

import (
	"os"
	"syscall"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/testwait"
)

func testInterrupter(t *testing.T, mode, termGrace string) *interrupter {
	t.Helper()
	cfg := &config.Config{Settings: config.Settings{Lanes: 1, Interrupt: mode, InterruptGrace: "1h", TermGrace: termGrace}}
	o := Options{Root: t.TempDir(), Cfg: cfg, RunID: "r-test"}
	disp := display.New(display.Options{Out: notifyFile(t), Plain: true}, nil)
	ir := newInterrupter(o, disp, newSlots(0, nil), newLockTable())
	t.Cleanup(ir.stop)
	return ir
}

func TestEscalation(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		sigs       []syscall.Signal
		want       int
	}{
		{"first Ctrl-C is graceful", "", []syscall.Signal{syscall.SIGINT}, levelGraceful},
		{"second forces", "", []syscall.Signal{syscall.SIGINT, syscall.SIGINT}, levelForce},
		{"third kills", "", []syscall.Signal{syscall.SIGINT, syscall.SIGINT, syscall.SIGINT}, levelKill},
		{"immediate: first forces", config.InterruptImmediate, []syscall.Signal{syscall.SIGINT}, levelForce},
		{"immediate: second kills", config.InterruptImmediate, []syscall.Signal{syscall.SIGINT, syscall.SIGINT}, levelKill},
		{"SIGHUP forces at once", "", []syscall.Signal{syscall.SIGHUP}, levelForce},
		{"SIGHUP after graceful forces", "", []syscall.Signal{syscall.SIGINT, syscall.SIGHUP}, levelForce},
		{"SIGTERM is graceful", "", []syscall.Signal{syscall.SIGTERM}, levelGraceful},
		{"SIGTERM then Ctrl-C forces", "", []syscall.Signal{syscall.SIGTERM, syscall.SIGINT}, levelForce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ir := testInterrupter(t, tc.mode, "1h")
			for _, s := range tc.sigs {
				ir.handle(s)
			}
			if ir.level != tc.want {
				t.Fatalf("level %d, want %d", ir.level, tc.want)
			}
			if !ir.stopping() || ir.Notice() == "" {
				t.Fatalf("stopping %v notice %q", ir.stopping(), ir.Notice())
			}
		})
	}
}

func TestTermGraceForces(t *testing.T) {
	ir := testInterrupter(t, "", "50ms")
	ir.handle(syscall.SIGTERM)
	// Escalating prints the force-quit notice.
	testwait.Until(t, "SIGTERM escalating to force after term_grace", &displayed, func() bool {
		ir.mu.Lock()
		defer ir.mu.Unlock()
		return ir.level == levelForce
	})
}

// displayed changes whenever a test's display or signal catcher writes.
var displayed testwait.Signal

// notifyFile is a file whose writes are discarded and notify displayed
// (a display writes to an *os.File).
func notifyFile(t *testing.T) *os.File {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer r.Close()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				displayed.Notify()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { w.Close() })
	return w
}

// notifyWriter discards what it's given and notifies s.
type notifyWriter struct{ s *testwait.Signal }

func (w notifyWriter) Write(p []byte) (int, error) {
	w.s.Notify()
	return len(p), nil
}
