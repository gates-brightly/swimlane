package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Pause requests (swim pause): a lane's round holds before its next step
// while one is in place, and goes on once it's gone. Unlike a stop, a
// pause ends nothing. A request names the run it's for, so one left by an
// earlier run is ignored (and lane_init clears it), as a stop request is.
// The repo-wide pause (swim pause --all) holds every lane and keeps queue
// workers from claiming batches.

// PauseReq is a pause request: the run it's for, who asked and when, and
// the note they gave.
type PauseReq struct {
	Run  string `yaml:"run,omitempty"`
	By   string `yaml:"by"`
	At   string `yaml:"at"`
	Note string `yaml:"note,omitempty"`
}

// PausePath is lane n's pause request (.swim/laneN.pause).
func PausePath(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.pause", n))
}

// RepoPausePath is the repo-wide pause (.swim/pause).
func RepoPausePath(root string) string { return filepath.Join(root, ".swim", "pause") }

// ResumedPath is who let lane n's round go on (swim unpause leaves it for
// the round to record, and the round removes it).
func ResumedPath(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.resumed", n))
}

func writePause(path string, r PauseReq) error {
	if r.At == "" {
		r.At = time.Now().UTC().Format(time.RFC3339)
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	data, _ := yaml.Marshal(r)
	return os.WriteFile(path, data, 0o644)
}

func readPause(path string) (*PauseReq, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var r PauseReq
	if yaml.Unmarshal(data, &r) != nil {
		return &PauseReq{}, true // unreadable: still a pause
	}
	return &r, true
}

// RequestPause asks lane n (in run) to hold before its next step.
func RequestPause(root string, n int, r PauseReq) error { return writePause(PausePath(root, n), r) }

// RequestRepoPause sets the repo-wide pause.
func RequestRepoPause(root string, r PauseReq) error { return writePause(RepoPausePath(root), r) }

// LanePause is lane n's own pause request for run, if one is in place: a
// request for another run doesn't count; an empty run matches any.
func LanePause(root string, n int, run string) (*PauseReq, bool) {
	r, ok := readPause(PausePath(root, n))
	if !ok || run != "" && r.Run != "" && strings.TrimSpace(r.Run) != run {
		return nil, false
	}
	return r, true
}

// RepoPause is the repo-wide pause, if it's on.
func RepoPause(root string) (*PauseReq, bool) { return readPause(RepoPausePath(root)) }

// Paused is what holds lane n's round in run now: its own pause request,
// or the repo-wide pause.
func Paused(root string, n int, run string) (*PauseReq, bool) {
	if r, ok := LanePause(root, n, run); ok {
		return r, true
	}
	return RepoPause(root)
}

// ReplacedPath is the job a paused round of lane n was ended for (swim run
// --replace), for the round's chain entry.
func ReplacedPath(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.replaced", n))
}

// ClearPause removes lane n's pause request.
func ClearPause(root string, n int) { os.Remove(PausePath(root, n)) }

// ClearRepoPause lifts the repo-wide pause.
func ClearRepoPause(root string) { os.Remove(RepoPausePath(root)) }
