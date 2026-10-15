package cli

import (
	"os"
	"time"

	"github.com/gates-brightly/swimlane/internal/chime"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// chimeWhenDone returns the launcher's Finished hook: it chimes once after
// the summary, per config (chime, chime_style, chime_min_s) and --chime /
// --no-chime. A chime never changes the exit code.
func chimeWhenDone(cfg *config.Config, rf runFlags) func(launcher.Finish) {
	mode, style, minS := cfg.ChimeSettings()
	switch {
	case rf.chime:
		mode = config.ChimeOn
	case rf.noChime:
		mode = config.ChimeOff
	}
	return func(f launcher.Finish) {
		result := chime.Passed
		switch {
		case f.Interrupted:
			result = chime.Interrupted
		case f.Code != 0:
			result = chime.Failed
		}
		// With --yaml, stdout is the event stream: chime on stderr.
		out := os.Stdout
		if rf.yaml {
			out = os.Stderr
		}
		ringChime(out, mode, style, minS, result, f.Elapsed, f.Counts)
	}
}

// chimeBatch chimes for a batch swim watch ran (a queued job), by the same
// settings as swim run: chime on rings for every finished batch, failure
// for one that didn't pass. The worker's terminal is the watch screen's.
func chimeBatch(cfg *config.Config, root string, bp *batchProc, outcome string) {
	mode, style, minS := cfg.ChimeSettings()
	result := chime.Passed
	switch outcome {
	case "passed":
	case "interrupted":
		result = chime.Interrupted
	default:
		result = chime.Failed
	}
	counts := map[string]int{}
	if e, err := queue.Find(root, bp.e.ID); err == nil && e.Result != nil {
		for _, l := range e.Result.Lanes {
			counts[l.Result]++
		}
	}
	ringChime(os.Stdout, mode, style, minS, result, time.Since(bp.started), counts)
}

// ringChime rings once for a finished run or batch, if chime.Should says
// so: the bell on a terminal (twice for a failure), plus a sound or a
// desktop notification per chime_style.
func ringChime(out *os.File, mode config.Chime, style string, minS int, result chime.Result, elapsed time.Duration, counts map[string]int) {
	tty := ui.IsTTY(out)
	in := chime.Input{Mode: mode, Style: style, MinS: minS, Result: result, Elapsed: elapsed, TTY: tty, CI: chime.IsCI(os.Getenv)}
	if !chime.Should(in) {
		return
	}
	msg := chime.Message(counts[status.Passed], counts[status.Failed], counts[status.Skipped], counts[status.Interrupted], display.Elapsed(elapsed))
	chime.New(out, tty).Ring(style, result != chime.Passed, msg)
}
