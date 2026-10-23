package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/hub"
)

// The hub's Trash: an operator's command, pinned to the lane's job, taken
// only when swim.yml's headless.allow lists trash. A viewer can't send it,
// a pin to a job the lane no longer holds moves nothing, and the worker's
// record names the hub user.
func TestHeadlessHubTrash(t *testing.T) {
	r, url, _, id, alice, vic := commandRepo(t)
	r.script(1, "keep", `run "one" true`)
	r.script(2, "drop", `run "two" true`)
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	t.Cleanup(func() { r.swim("watch", "--headless", "--stop") })
	via := " via " + url + ": "

	if _, err := vic.Send(id, hub.Command{Kind: hub.KindTrash, Lane: 2, Pin: hub.Pin{Job: "0123456789abcdef"}}); err == nil || !strings.Contains(err.Error(), "operator role") {
		t.Errorf("a viewer trashed: %v", err)
	}
	if _, err := alice.Send(id, hub.Command{Kind: hub.KindTrash, Lane: 2}); err == nil || !strings.Contains(err.Error(), "the job it holds") {
		t.Errorf("an unpinned trash was accepted: %v", err)
	}

	// Under read access: refused, and the hub isn't offered a pin.
	contains(t, "access read", r.mustSwim("auth", "login", url, "--access", "read"), "access read", "access is now read")
	alice.Send(id, hub.Command{Kind: hub.KindTrash, Lane: 2, Pin: hub.Pin{Job: "0123456789abcdef"}})
	waitLog(t, r, "hub trash by alice"+via+"refused: not allowed here: this swimmer's access is read")
	if l := laneOnHub(t, url, id, 2); l.TrashJob != "" {
		t.Errorf("trash_job published while trash isn't allowed: %q", l.TrashJob)
	}

	r.mustSwim("auth", "login", url, "--access", "readwrite")
	waitFor(t, func() bool { return laneOnHub(t, url, id, 2).TrashJob != "" })
	pin := laneOnHub(t, url, id, 2).TrashJob

	// Pinned to a job the lane doesn't hold: refused, nothing moves.
	alice.Send(id, hub.Command{Kind: hub.KindTrash, Lane: 2, Pin: hub.Pin{Job: "0123456789abcdef"}})
	waitLog(t, r, "hub trash by alice"+via+"refused: swim 2 holds "+pin[:8]+" now, not 01234567")
	if _, err := os.Stat(filepath.Join(r.root, "lane.2.sh")); err != nil {
		t.Fatal("a stale trash moved lane.2.sh")
	}

	alice.Send(id, hub.Command{Kind: hub.KindTrash, Lane: 2, Pin: hub.Pin{Job: pin}})
	waitLog(t, r, "hub trash by alice"+via+"done: trashed swim 2 (.swim/cleaned/")
	if _, err := os.Stat(filepath.Join(r.root, "lane.2.sh")); err == nil {
		t.Error("lane.2.sh still in place")
	}
	if _, err := os.Stat(filepath.Join(r.root, "lane.1.sh")); err != nil {
		t.Error("lane.1.sh moved")
	}
	waitLog(t, r, "(from hub) by hub:alice via "+url)
	contains(t, "trash list", r.mustSwim("trash"), "swim 2", "hub")
}
