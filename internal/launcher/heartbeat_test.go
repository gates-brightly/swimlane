package launcher

import (
	"testing"
	"time"
)

// A lane quiet for a beat is reported, once per beat; a lane that writes
// within each beat never is.
func TestHeartbeatSilent(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	now := t0
	h := &heartbeat{last: map[int]time.Time{}, started: map[int]time.Time{}, now: func() time.Time { return now }}
	h.begin(1, t0) // quiet
	h.begin(2, t0) // chatty
	beat := time.Second
	reported := map[int]int{}
	for i := 1; i <= 25; i++ { // 2.5 beats, in tenths
		now = t0.Add(time.Duration(i) * beat / 10)
		if i%2 == 0 {
			h.saw(2) // a line every 0.2 beats
		}
		for n := range h.silent(beat) {
			reported[n]++
		}
	}
	if reported[1] != 2 || reported[2] != 0 {
		t.Errorf("heartbeats: quiet lane %d (want 2), chatty lane %d (want 0)", reported[1], reported[2])
	}
	h.end(1)
	now = now.Add(10 * beat)
	if _, ok := h.silent(beat)[1]; ok {
		t.Error("a lane that ended still gets heartbeats")
	}
}
