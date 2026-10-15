package launcher

import (
	"os"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
)

// statusWindow is how long a skipped lane's status waits for others to
// share its write.
const statusWindow = 50 * time.Millisecond

// statusBatch writes the status of lanes a run skips together: when a lane
// fails or the run is stopped, everything after it is skipped at once, and
// one status.yml write per lane (each synced to disk) took seconds for a
// large graph on macOS, where a sync flushes the drive. Writes wait at
// most statusWindow; flush writes what's left before the run's summary.
type statusBatch struct {
	root   string
	lanes  int
	window time.Duration

	mu      sync.Mutex
	pending map[int][]func(*status.Lane)
	order   []int
	timer   *time.Timer
}

func newStatusBatch(root string, lanes int) *statusBatch {
	return &statusBatch{root: root, lanes: lanes, window: statusWindow, pending: map[int][]func(*status.Lane){}}
}

// update queues fn for lane n's status, as status.Update would apply it.
func (b *statusBatch) update(n int, fn func(*status.Lane)) {
	if os.Getenv("SWIM_CHECK_ONLY") == "1" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pending[n]; !ok {
		b.order = append(b.order, n)
	}
	b.pending[n] = append(b.pending[n], fn)
	if n > b.lanes {
		b.lanes = n
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(b.window, b.flush)
	}
}

// flush writes every queued update in one status.yml write.
func (b *statusBatch) flush() {
	b.mu.Lock()
	pending, order, lanes := b.pending, b.order, b.lanes
	b.pending, b.order = map[int][]func(*status.Lane){}, nil
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	b.mu.Unlock()
	if len(order) == 0 {
		return
	}
	status.UpdateFile(b.root, lanes, func(f *status.File) error {
		for _, n := range order {
			l := f.Mine(n)
			for _, fn := range pending[n] {
				fn(l)
			}
		}
		return nil
	})
}
