package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// headlessRepo is a repo enrolled with a fresh hub (its URL), with HOME (and
// XDG_CONFIG_HOME, from newRepo) in temp dirs: --install never touches the
// real user's home.
func headlessRepo(t *testing.T) (*repo, string) {
	t.Helper()
	r := newRepo(t, "")
	r.env = append(r.env, "HOME="+t.TempDir())
	url, stop := startHub(t, r, t.TempDir(), "127.0.0.1:0")
	t.Cleanup(stop)
	r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "headless")
	// Whatever a test leaves running, it doesn't outlive it.
	t.Cleanup(func() {
		if pid := headlessPid(r); pid > 0 {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return r, url
}

// headlessPid is the pid in .swim/headless.pid (0 for none).
func headlessPid(r *repo) int {
	data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "headless.pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func TestHeadlessNeedsAHub(t *testing.T) {
	r := newRepo(t, "")
	out, code := r.swim("watch", "--headless")
	if code == 0 || !strings.Contains(out, "needs a hub") {
		t.Fatalf("not enrolled: exit %d\n%s", code, out)
	}
	if out, code := r.swim("watch", "--detach"); code != 2 || !strings.Contains(out, "go with --headless") {
		t.Errorf("--detach without --headless: exit %d\n%s", code, out)
	}
}

func TestHeadlessDetachStatusStop(t *testing.T) {
	r, url := headlessRepo(t)
	out := r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	pid := headlessPid(r)
	contains(t, "--detach", out, fmt.Sprintf("pid %d", pid), ".swim/headless.log")
	if pid == 0 {
		t.Fatal("no .swim/headless.pid")
	}

	// One per repo: a second exits 2 and names the first.
	for _, args := range [][]string{{"watch", "--headless"}, {"watch", "--headless", "--detach"}} {
		out, code := r.swim(args...)
		if code != 2 || !strings.Contains(out, fmt.Sprintf("pid %d holds .swim/headless.lock", pid)) {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
	contains(t, "--status", r.mustSwim("watch", "--headless", "--status"), fmt.Sprintf("running, pid %d", pid), "service: none installed")
	// The hub knows nobody is at this worker's terminal.
	waitFor(t, func() bool {
		resp, err := http.Get(url + "/api/workers")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(body), `"headless":{"mode":"detached"`)
	})

	// SIGHUP mid-lane reopens the log (logrotate moved it) and the lane
	// runs on to pass.
	r.script(1, "slow", `run "slow" bash -c 'for i in 1 2 3 4 5 6; do echo tick-$i; sleep 0.3; done'`)
	r.mustSwim("queue", "add", "1")
	waitFor(t, func() bool { return r.status().Get(1).CurrentStep == "slow" })
	logPath := filepath.Join(r.root, ".swim", "headless.log")
	if err := os.Rename(logPath, logPath+".moved"); err != nil {
		t.Fatal(err)
	}
	syscall.Kill(pid, syscall.SIGHUP)
	deadline := time.Now().Add(10 * time.Second)
	for len(doneEntries(t, r)) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if e := doneEntries(t, r); len(e) != 1 || e[0].Result.Result != "passed" {
		t.Fatalf("the batch after SIGHUP: %+v", e)
	}
	// The worker writes "passed; consumed" just after it consumes the
	// entry: wait for the line, not only the entry.
	var data []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if data, _ = os.ReadFile(logPath); strings.Contains(string(data), "passed; consumed") {
			break
		}
	}
	contains(t, "the reopened log", string(data), "SIGHUP: reopened .swim/headless.log", "tick-6", "passed; consumed")
	if !processAlive(pid) {
		t.Fatal("SIGHUP stopped the worker")
	}

	out = r.mustSwim("watch", "--headless", "--stop")
	contains(t, "--stop", out, fmt.Sprintf("SIGTERM to the headless worker (pid %d)", pid), "stopped")
	if headlessPid(r) != 0 {
		t.Error("the pidfile outlived a clean stop")
	}
	out, code := r.swim("watch", "--headless", "--status")
	if code != 3 || !strings.Contains(out, "not running") {
		t.Errorf("--status after --stop: exit %d\n%s", code, out)
	}
	contains(t, "--stop again", r.mustSwim("watch", "--headless", "--stop"), "no headless worker running")
}

// A worker killed outright leaves its pidfile behind: --status calls it
// stale, the lock is free, and a new worker starts.
func TestHeadlessKilled(t *testing.T) {
	r, _ := headlessRepo(t)
	r.mustSwim("watch", "--headless", "--detach")
	pid := headlessPid(r)
	syscall.Kill(pid, syscall.SIGKILL)
	waitFor(t, func() bool { return !processAlive(pid) })
	out, code := r.swim("watch", "--headless", "--status")
	if code != 3 || !strings.Contains(out, fmt.Sprintf("stale .swim/headless.pid: pid %d is gone", pid)) || !strings.Contains(out, "lock is free") {
		t.Errorf("--status after kill -9: exit %d\n%s", code, out)
	}
	r.mustSwim("watch", "--headless", "--detach")
	if p := headlessPid(r); p == pid || p == 0 {
		t.Fatalf("no new worker after a stale one: pid %d", p)
	}
	r.mustSwim("watch", "--headless", "--stop")
}

func TestHeadlessInstall(t *testing.T) {
	r, _ := headlessRepo(t)
	var home, xdg string
	for _, kv := range r.env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
		if v, ok := strings.CutPrefix(kv, "XDG_CONFIG_HOME="); ok {
			xdg = v
		}
	}
	units, _ := filepath.Glob(filepath.Join(xdg, "systemd", "user", "swim-*.service"))
	out, code := r.swim("watch", "--headless", "--install", "systemd", "-y")
	if code == 0 || !strings.Contains(out, "--i-mean-it") {
		t.Fatalf("-y without --i-mean-it: exit %d\n%s", code, out)
	}
	if units, _ = filepath.Glob(filepath.Join(xdg, "systemd", "user", "swim-*.service")); len(units) != 0 {
		t.Fatalf("a refused install wrote %v", units)
	}

	out = r.mustSwim("watch", "--headless", "--install", "systemd", "-y", "--i-mean-it")
	contains(t, "--install systemd", out, "systemctl --user enable --now swim-", "loginctl enable-linger", "swim doesn't start it")
	if units, _ = filepath.Glob(filepath.Join(xdg, "systemd", "user", "swim-*.service")); len(units) != 1 {
		t.Fatalf("units: %v", units)
	}
	data, _ := os.ReadFile(units[0])
	contains(t, "the unit", string(data), "WorkingDirectory="+r.root, `"watch" "--headless" "-y"`, "TimeoutStopSec=40", "KillMode=mixed",
		`Environment="SWIM_USER=`, `Environment="PATH=`, "# -y: this worker approves every guard")

	out = r.mustSwim("watch", "--headless", "--install", "launchd")
	contains(t, "--install launchd", out, "launchctl bootstrap gui/")
	plists, _ := filepath.Glob(filepath.Join(home, "Library", "LaunchAgents", "com.swim.*.plist"))
	if len(plists) != 1 {
		t.Fatalf("plists: %v", plists)
	}
	data, _ = os.ReadFile(plists[0])
	contains(t, "the plist", string(data), "<string>"+r.root+"</string>", "<string>--headless</string>", "<key>ExitTimeOut</key>")
	if fi, err := os.Stat(filepath.Join(home, "Library", "Logs", "swim")); err != nil || !fi.IsDir() {
		t.Error("no ~/Library/Logs/swim")
	}

	out, _ = r.swim("watch", "--headless", "--status")
	contains(t, "--status", out, "service: systemd unit "+units[0], "service: launchd unit "+plists[0])
	out = r.mustSwim("watch", "--headless", "--uninstall")
	contains(t, "--uninstall", out, "removed "+units[0], "removed "+plists[0], "systemctl --user daemon-reload")
	for _, p := range []string{units[0], plists[0]} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still there", p)
		}
	}
}

func processAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// A killed worker nobody reaped yet is a zombie: gone all the same.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		i := strings.LastIndexByte(string(data), ')')
		return i < 0 || !strings.HasPrefix(string(data[i+1:]), " Z")
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
