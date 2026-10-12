package launcher

import (
	"fmt"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Busy says why swim isn't idle in this repo ("" when it is): a lane
// script is running, or a swim run is still waiting, queued or locked on
// lanes (status.yml shows its launcher's pid, and that process lives).
func Busy(root string, cfg *config.Config) string {
	for n := 1; n <= cfg.Lanes; n++ {
		if pid, ok := lane.Running(root, n); ok {
			return fmt.Sprintf("swim %d is running (pid %d)", n, pid)
		}
	}
	st, _ := status.Load(root)
	if st == nil {
		return ""
	}
	for _, l := range st.Lanes {
		switch l.State {
		case status.Waiting, status.Queued, status.Locked, status.Running:
			if l.PID > 0 && lane.Alive(l.PID) {
				return fmt.Sprintf("swim %d is %s in another swim run (pid %d)", l.Lane, l.State, l.PID)
			}
		}
	}
	return ""
}
