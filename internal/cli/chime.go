package cli

import (
	"os"

	"github.com/gates-brightly/swimlane/internal/chime"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/launcher"
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
		tty := ui.IsTTY(out)
		in := chime.Input{Mode: mode, Style: style, MinS: minS, Result: result, Elapsed: f.Elapsed, TTY: tty, CI: chime.IsCI(os.Getenv)}
		if !chime.Should(in) {
			return
		}
		msg := chime.Message(f.Counts[status.Passed], f.Counts[status.Failed], f.Counts[status.Skipped], f.Counts[status.Interrupted], display.Elapsed(f.Elapsed))
		chime.New(out, tty).Ring(style, result != chime.Passed, msg)
	}
}
