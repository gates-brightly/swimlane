package e2e

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

type hubWorkers struct {
	Workers []struct {
		ID, Name, Root, Activity string
		Stale                    bool
		Lanes                    []struct {
			Lane        int
			State, Step string
			Tail        []string
		}
		Queue []struct{ State, Result string }
	} `json:"enrollments"`
}

func hubGet(t *testing.T, url string) hubWorkers {
	t.Helper()
	resp, err := http.Get(url + "/api/enrollments")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var w hubWorkers
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestHub(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	hub := exec.Command(bin, "hub", "--addr", "127.0.0.1:0", "--token", "t0ken")
	hub.Env = r.env
	hub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := hub.StdoutPipe()
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { syscall.Kill(-hub.Process.Pid, syscall.SIGTERM); hub.Wait() }()
	sc := bufio.NewScanner(out)
	sc.Scan()
	url := regexp.MustCompile(`http://[0-9.:]+`).FindString(sc.Text())
	if url == "" {
		t.Fatalf("no hub URL in %q", sc.Text())
	}
	go func() {
		for sc.Scan() {
		}
	}()

	if out, code := r.swim("hub", "enroll", url, "--token", "wrong"); code == 0 || !strings.Contains(out, "401") {
		t.Fatalf("wrong token: %d %s", code, out)
	}
	contains(t, "enroll", r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "billing"), "enrolled", "as billing")
	if w := hubGet(t, url); len(w.Workers) != 1 || w.Workers[0].Name != "billing" || !strings.Contains(w.Workers[0].Activity, "not watching") || len(w.Workers[0].Lanes) != 0 {
		t.Fatalf("after enroll: %+v", w)
	}
	contains(t, "status", r.mustSwim("hub", "status"), "enrolled with "+url+" as billing", "reachable")

	r.script(1, "slow", `run "slow" bash -c 'for i in 1 2 3 4 5 6; do echo tick-$i; sleep 0.4; done'`)
	r.mustSwim("queue", "1")
	watch := r.cmd(bin, "watch", "--once", "--interval", "300ms")
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	// Mid-run: the hub shows the batch running and the lane's live tail.
	waitFor(t, func() bool {
		w := hubGet(t, url).Workers
		if len(w) != 1 || !strings.HasPrefix(w[0].Activity, "running q-") {
			return false
		}
		for _, l := range w[0].Lanes {
			if l.Lane == 1 && l.State == "running" && l.Step == "slow" && len(l.Tail) > 0 && strings.Contains(strings.Join(l.Tail, "\n"), "| tick-") {
				return true
			}
		}
		return false
	})
	if err := watch.Wait(); err != nil {
		t.Fatalf("watch: %v", err)
	}
	w := hubGet(t, url).Workers[0]
	if w.Activity != "stopped" || len(w.Queue) != 1 || w.Queue[0].State != "done" || w.Queue[0].Result != "passed" {
		t.Errorf("after the batch: %+v", w)
	}
	resp, _ := http.Get(url + "/classic")
	sc2 := bufio.NewScanner(resp.Body)
	page := ""
	for sc2.Scan() {
		page += sc2.Text() + "\n"
	}
	resp.Body.Close()
	contains(t, "dashboard", page, "swim hub", "billing", "stopped")

	contains(t, "leave", r.mustSwim("hub", "leave"), "left "+url)
	if w := hubGet(t, url); len(w.Workers) != 0 {
		t.Errorf("still listed after leave: %+v", w)
	}
	time.Sleep(10 * time.Millisecond)
}
