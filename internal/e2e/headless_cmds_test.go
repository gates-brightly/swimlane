package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/hub"
)

// commandRepo is a repo enrolled with a hub that keeps its state in a known
// dir (for its audit log), with an operator (alice) and a viewer (vic).
func commandRepo(t *testing.T) (r *repo, url, state, id string, alice, vic *hub.UserClient) {
	t.Helper()
	r = newRepo(t, "")
	r.env = append(r.env, "HOME="+t.TempDir())
	state = t.TempDir()
	url, stop := startHub(t, r, state, "127.0.0.1:0")
	t.Cleanup(stop)
	r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "headless")
	r.admit(t, url, "headless")
	t.Cleanup(func() {
		if pid := headlessPid(r); pid > 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	var hf struct{ ID string }
	data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "hub.yml"))
	yaml.Unmarshal(data, &hf)
	user := func(name, role string) *hub.UserClient {
		out := r.mustSwim("hub", "user", "add", name, "--role", role, "--hub", url, "--admin-token", "adm1n")
		tok := regexp.MustCompile(`(?m)^  ([0-9a-f]{48})$`).FindStringSubmatch(out)
		if tok == nil {
			t.Fatalf("no token in:\n%s", out)
		}
		u, _ := hub.NewUser(url, tok[1], nil)
		return u
	}
	return r, url, state, hf.ID, user("alice", "operator"), user("vic", "viewer")
}

// waitLog waits (up to 10s) for .swim.log to hold want.
func waitLog(t *testing.T, r *repo, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
		if strings.Contains(string(data), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	hl, _ := os.ReadFile(filepath.Join(r.root, ".swim", "headless.log"))
	t.Fatalf(".swim.log never had %q:\n%s\nheadless.log:\n%s", want, data, hl)
}

// laneOnHub is lane n as the hub has it from the worker's last report.
func laneOnHub(t *testing.T, url, id string, n int) hub.Lane {
	t.Helper()
	resp, err := http.Get(url + "/api/enrollments")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Workers []hub.View `json:"enrollments"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	for _, w := range out.Workers {
		if w.ID == id {
			for _, l := range w.Lanes {
				if l.Lane == n {
					return l
				}
			}
		}
	}
	return hub.Lane{}
}

func TestHeadlessHubCommands(t *testing.T) {
	long(t)
	r, url, state, id, alice, vic := commandRepo(t)
	const planted = "planted-s3cr3t-value-9f8e7d"
	r.env = append(r.env, "DEPLOY_TOKEN="+planted)
	r.script(1, "slow", `run "slow" bash -c 'echo token=$DEPLOY_TOKEN; sleep 3'
run "after" sleep 10`)
	r.script(2, "quick", `run "quick" echo hello from the hub`)
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	via := " via " + url + ": "

	// A viewer can't interrupt: the hub refuses it before any worker sees it.
	if _, err := vic.Send(id, hub.Command{Kind: hub.KindInterrupt, Lane: 1, Pin: hub.Pin{Run: "r-x"}}); err == nil || !strings.Contains(err.Error(), "operator role") {
		t.Errorf("a viewer interrupted: %v", err)
	}

	// Interrupt: pinned to an old run it's refused as stale; to the
	// current one it stops the lane.
	r.mustSwim("queue", "add", "1")
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "slow" })
	run := r.status().Get(1).Run
	alice.Send(id, hub.Command{Kind: hub.KindInterrupt, Lane: 1, Pin: hub.Pin{Run: "r-old"}})
	waitLog(t, r, "hub interrupt by alice"+via+"refused: stale: swim 1 is now run "+run+", not r-old")
	alice.Send(id, hub.Command{Kind: hub.KindInterrupt, Lane: 1, Pin: hub.Pin{Run: run}})
	waitLog(t, r, "hub interrupt by alice"+via+"done: swim 1: stopping after slow")
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 1 })
	if e := doneEntries(t, r)[0]; len(e.Result.Lanes) != 1 || e.Result.Lanes[0].Result != "interrupted" {
		t.Errorf("the interrupted batch: %+v", e.Result)
	}

	// Its log, uploaded on request: masked, the planted secret nowhere.
	vic.Send(id, hub.Command{Kind: hub.KindLog, Lane: 1, Pin: hub.Pin{Run: run}})
	waitLog(t, r, "hub log by vic"+via+"done: uploaded")
	text, err := vic.Log(id, run, 1)
	if err != nil || !strings.Contains(text, "token=") || !strings.Contains(text, "slow") {
		t.Fatalf("uploaded log: %v\n%s", err, text)
	}
	if strings.Contains(text, planted) {
		t.Fatalf("the planted secret reached the hub:\n%s", text)
	}

	// run is refused until swim.yml allows it.
	sha := strings.Repeat("a", 64)
	alice.Send(id, hub.Command{Kind: hub.KindRun, Lane: 2, Pin: hub.Pin{SHA256: sha, Manifest: sha}})
	waitLog(t, r, "hub run by alice"+via+"refused: not allowed here: swim.yml's headless.allow is [answer, interrupt, drain, log]")
	r.write("swim.yml", "headless:\n  allow: [interrupt, drain, log, run]\n", 0o644)
	var l2 hub.Lane
	waitFor(t, func() bool { l2 = laneOnHub(t, url, id, 2); return l2.Script != "" && l2.Manifest != "" })
	// Pinned to another script, or another manifest: refused as stale.
	alice.Send(id, hub.Command{Kind: hub.KindRun, Lane: 2, Pin: hub.Pin{SHA256: sha, Manifest: l2.Manifest}})
	waitLog(t, r, "refused: stale: swim 2's script is now "+l2.Script[:12])
	alice.Send(id, hub.Command{Kind: hub.KindRun, Lane: 2, Pin: hub.Pin{SHA256: l2.Script, Manifest: sha}})
	waitLog(t, r, "refused: stale: swim 2's manifest is now "+l2.Manifest[:12])
	// As shown: an unsigned entry by the hub's user, with no approvals.
	alice.Send(id, hub.Command{Kind: hub.KindRun, Lane: 2, Pin: hub.Pin{SHA256: l2.Script, Manifest: l2.Manifest}})
	waitLog(t, r, "hub run by alice"+via+"done: queued")
	waitFor(t, func() bool { return len(doneEntries(t, r)) == 2 })
	for _, e := range doneEntries(t, r) {
		if e.Lanes[0].Lane != 2 {
			continue
		}
		if e.By != "hub:alice via "+url || e.QueueSig != "" || len(e.Approve) != 0 || e.ApproveAll || e.Result.Result != "passed" || e.Lanes[0].Manifest != l2.Manifest {
			t.Errorf("the hub's run: %+v", e)
		}
	}

	// headless.logs: off refuses logs.
	r.write("swim.yml", "headless:\n  allow: [interrupt, drain, log, run]\n  logs: off\n", 0o644)
	vic.Send(id, hub.Command{Kind: hub.KindLog, Lane: 1, Pin: hub.Pin{Run: run}})
	waitLog(t, r, "hub log by vic"+via+"refused: swim.yml sets headless.logs: off")

	// Drain: the batch in hand finishes, then the worker exits.
	r.script(3, "short", `run "short" bash -c 'sleep 1; echo drained-ok'`)
	r.mustSwim("queue", "add", "3")
	waitFor(t, func() bool { return r.status().Get(3).CurrentStep == "short" })
	pid := headlessPid(r)
	alice.Send(id, hub.Command{Kind: hub.KindDrain})
	waitLog(t, r, "hub drain by alice"+via+"done: the worker stops after the batch in hand")
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatal("the worker didn't exit after the drained batch")
	}
	if e := doneEntries(t, r); len(e) != 3 || !strings.Contains(r.log(3), "drained-ok") {
		t.Errorf("the batch in hand didn't finish: %d done", len(e))
	}

	// A command that waited past command_ttl while the worker was down is
	// acknowledged expired, never carried out.
	r.write("swim.yml", "headless:\n  allow: [interrupt, drain, log, run]\n  command_ttl: 1s\n", 0o644)
	alice.Send(id, hub.Command{Kind: hub.KindDrain})
	time.Sleep(2 * time.Second)
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	waitLog(t, r, "hub drain by alice"+via+"expired: it expired at")
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(state, "audit.log"))
		return strings.Contains(string(data), "acked drain") && strings.Contains(string(data), "expired it expired at")
	})
	if !processAlive(headlessPid(r)) {
		t.Error("an expired drain stopped the worker")
	}
	r.mustSwim("watch", "--headless", "--stop")

	// The worker's own record: every command in .swim.log and the chain,
	// and swim verify accepts the hub entries.
	out := r.mustSwim("verify")
	contains(t, "verify", out, "chain of custody")
	hist := r.mustSwim("history", "--oneline", "--no-pager")
	contains(t, "history", hist, "hub interrupt by alice", "hub run by alice", "hub log by vic")
	// Nobody logged in reads no commands.
	if resp, err := http.Get(url + "/api/enrollments/" + id + "/commands"); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("commands for nobody: %v %v", resp, err)
	}
}
