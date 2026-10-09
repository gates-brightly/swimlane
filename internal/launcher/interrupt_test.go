package launcher

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
)

func testInterrupter(t *testing.T, mode, termGrace string) *interrupter {
	t.Helper()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	t.Cleanup(func() { devnull.Close() })
	cfg := &config.Config{Settings: config.Settings{Lanes: 1, Interrupt: mode, InterruptGrace: "1h", TermGrace: termGrace}}
	o := Options{Root: t.TempDir(), Cfg: cfg, RunID: "r-test"}
	disp := display.New(display.Options{Out: devnull, Plain: true}, nil)
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ir.mu.Lock()
		lvl := ir.level
		ir.mu.Unlock()
		if lvl == levelForce {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("SIGTERM didn't escalate to force after term_grace")
}
