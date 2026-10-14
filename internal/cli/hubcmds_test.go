package cli

import (
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
)

// A command is taken once: a replayed seq or id is ignored, and a new hub
// epoch starts the seq over without letting an old id through again.
func TestAcceptCommandsReplay(t *testing.T) {
	h := &hubFile{}
	cmd := func(seq int, cid string) hub.Command { return hub.Command{Seq: seq, CID: cid, Kind: "drain"} }
	fresh, _, changed := acceptCommands(h, "e1", []hub.Command{cmd(2, "b"), cmd(1, "a")})
	if len(fresh) != 2 || fresh[0].CID != "a" || !changed || h.LastSeq != 2 || h.Epoch != "e1" {
		t.Fatalf("first: %+v %+v", fresh, h)
	}
	fresh, ignored, changed := acceptCommands(h, "e1", []hub.Command{cmd(2, "b"), cmd(1, "x"), cmd(3, "c")})
	if len(fresh) != 1 || fresh[0].CID != "c" || len(ignored) != 2 || !changed {
		t.Fatalf("replayed seqs: fresh %+v ignored %+v", fresh, ignored)
	}
	if fresh, _, changed := acceptCommands(h, "e1", []hub.Command{cmd(3, "c")}); len(fresh) != 0 || changed {
		t.Fatalf("the same again: %+v", fresh)
	}
	// A new epoch (the hub lost its state, or another hub): seq 1 again is
	// new, but an id taken before is not.
	fresh, ignored, _ = acceptCommands(h, "e2", []hub.Command{cmd(1, "a"), cmd(2, "d")})
	if len(fresh) != 1 || fresh[0].CID != "d" || len(ignored) != 1 || h.Epoch != "e2" || h.LastSeq != 2 {
		t.Fatalf("new epoch: fresh %+v ignored %+v %+v", fresh, ignored, h)
	}
	for i := 0; i < recentCIDs+10; i++ {
		acceptCommands(h, "e2", []hub.Command{cmd(10+i, string(rune('A'+i%26))+time.Now().String())})
	}
	if len(h.RecentCIDs) != recentCIDs {
		t.Errorf("recent ids: %d", len(h.RecentCIDs))
	}
}

// The worker caps a command's life with its own command_ttl (2m for
// interrupt), whatever the hub says, from when it was sent (never later
// than now).
func TestCommandDeadline(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{}
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	for _, c := range []struct {
		cmd  hub.Command
		want time.Duration
	}{
		{hub.Command{Kind: "run", At: at(0), Expires: at(time.Hour)}, 10 * time.Minute},
		{hub.Command{Kind: "run", At: at(0), Expires: at(time.Minute)}, time.Minute},
		{hub.Command{Kind: "interrupt", At: at(0), Expires: at(time.Hour)}, 2 * time.Minute},
		{hub.Command{Kind: "run", At: at(time.Hour), Expires: at(2 * time.Hour)}, 10 * time.Minute},
	} {
		end, err := commandDeadline(cfg, c.cmd, now)
		if err != nil || end.Sub(now) != c.want {
			t.Errorf("%+v: %s %v, want %s", c.cmd, end.Sub(now), err, c.want)
		}
	}
	cfg.Headless = &config.Headless{CommandTTL: "30s"}
	if end, _ := commandDeadline(cfg, hub.Command{Kind: "run", At: at(0), Expires: at(time.Hour)}, now); end.Sub(now) != 30*time.Second {
		t.Errorf("command_ttl 30s: %s", end.Sub(now))
	}
	if _, err := commandDeadline(cfg, hub.Command{Kind: "run"}, now); err == nil {
		t.Error("no send time")
	}
}
