package e2e

import (
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/hub"
)

// pause and unpause from the hub: pinned to the run the page showed, taken
// only with them in headless.allow, recorded with the hub user.
func TestHeadlessHubPause(t *testing.T) {
	r, url, _, id, alice, _ := commandRepo(t)
	r.script(1, "slow", `run "one" sleep 0.5
run "two" true`)
	r.write("swim.yml", "headless:\n  allow: [answer, interrupt, drain, log, pause, unpause]\n", 0o644)
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	t.Cleanup(func() { r.swim("watch", "--headless", "--stop") })
	via := " via " + url + ": "

	r.mustSwim("queue", "add", "1")
	waitFor(t, func() bool { return laneOf(r, 1).CurrentStep == "one" })
	run := laneOf(r, 1).Run
	alice.Send(id, hub.Command{Kind: hub.KindPause, Lane: 1, Pin: hub.Pin{Run: "r-old"}})
	waitLog(t, r, "hub pause by alice"+via+"refused: stale: swim 1 is now run "+run)
	alice.Send(id, hub.Command{Kind: hub.KindPause, Lane: 1, Pin: hub.Pin{Run: run}})
	waitLog(t, r, "hub pause by alice"+via+"done: swim 1: pausing after")
	waitFor(t, func() bool { return laneOf(r, 1).PausedAt != "" })
	if !strings.HasPrefix(laneOf(r, 1).PausedBy, "hub:alice via ") {
		t.Errorf("paused by %q", laneOf(r, 1).PausedBy)
	}
	waitFor(t, func() bool { return laneOnHub(t, url, id, 1).Paused != "" })
	alice.Send(id, hub.Command{Kind: hub.KindUnpause, Lane: 1, Pin: hub.Pin{Run: run}})
	waitLog(t, r, "hub unpause by alice"+via+"done: swim 1: resumed")
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 1 })
	contains(t, "log", r.log(1), "PAUSE  by hub:alice", "RESUME  by hub:alice", "PASS  two")
}
