package cli

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Under swim.yml's hub.logs: stream, swim watch sends its hub each lane's
// round as its log grows (hub/stream.go): every report, the round's
// complete lines, masked with the secrets known now, past what it already
// sent. The log gets a step's block once the step ends; the step running
// now is in the snapshot's tail. If masking changed what was sent (a
// secret became known), or the hub has another count (it restarted), the
// whole round goes again from 0. A lane's round goes until it has ended,
// with a last, final append; past hub.LogMax it stops.

// laneStream is what this worker has sent of one round's log.
type laneStream struct {
	sent  string // the masked text the hub has
	final bool   // the round ended and the hub was told
	off   bool   // over hub.LogMax: nothing more goes
}

type hubStreamer struct {
	lanes map[string]*laneStream // run + "\x00" + lane
}

func newHubStreamer() *hubStreamer { return &hubStreamer{lanes: map[string]*laneStream{}} }

// send streams every lane's round in this directory's status that has
// begun, stopping at the first failure (the next report tries again).
func (hs *hubStreamer) send(root string, cfg *config.Config, c *hub.Client) error {
	if cfg.HubLogs() != config.HubLogsStream {
		return nil
	}
	st, err := status.Load(root)
	if err != nil || st == nil {
		return err
	}
	mk := cfg.Masker(os.Environ())
	keep := map[string]bool{}
	for _, l := range st.Lanes {
		switch l.State {
		case status.Idle, status.Waiting, status.Queued, status.Locked, status.Skipped:
			continue // no round of this run in its log
		}
		if l.Run == "" {
			continue
		}
		key := l.Run + "\x00" + strconv.Itoa(l.Lane)
		keep[key] = true
		ls := hs.lanes[key]
		if ls == nil {
			ls = &laneStream{}
			hs.lanes[key] = ls
		}
		if ls.final || ls.off {
			continue
		}
		text, finished := roundText(lane.Log(root, l.Lane), l.Run)
		if text == "" {
			continue
		}
		text = mk.String(text)
		if len(text) > hub.LogMax {
			ls.off = true
			continue
		}
		final := finished && l.State != status.Running
		if !strings.HasPrefix(text, ls.sent) {
			ls.sent = "" // masking changed what went: all of it again
		}
		if len(text) == len(ls.sent) && !final {
			continue
		}
		err := c.StreamLog(l.Run, l.Lane, len(ls.sent), []byte(text[len(ls.sent):]), final)
		if errors.Is(err, hub.ErrStreamOffset) && ls.sent != "" {
			err = c.StreamLog(l.Run, l.Lane, 0, []byte(text), final)
		}
		if err != nil {
			return err
		}
		ls.sent, ls.final = text, final
		if final {
			ls.sent = "" // nothing more goes: don't hold it
		}
	}
	for k := range hs.lanes {
		if !keep[k] {
			delete(hs.lanes, k)
		}
	}
	return nil
}

// roundText is run's rounds in the lane log at path, complete lines only
// (a line still being written waits for the next report), and whether
// the last of them has ended.
func roundText(path, run string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	end := strings.LastIndexByte(string(data), '\n')
	if end < 0 {
		return "", false
	}
	var rounds [][]string
	for _, line := range strings.Split(string(data[:end]), "\n") {
		if strings.HasPrefix(line, logparse.RoundMark) || strings.HasPrefix(line, "=== ROUND START") {
			rounds = append(rounds, nil)
		}
		if len(rounds) > 0 {
			rounds[len(rounds)-1] = append(rounds[len(rounds)-1], line)
		}
	}
	var lines, last []string
	for _, r := range rounds {
		if roundMatches(r, run) {
			lines, last = append(lines, r...), r
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	finished := false
	if r, err := logparse.ParseRound(last); err == nil {
		finished = r.Finished
	}
	return strings.Join(lines, "\n") + "\n", finished
}
