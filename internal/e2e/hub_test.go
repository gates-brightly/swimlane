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
	hub := exec.Command(bin, "hub", "--addr", "127.0.0.1:0", "--token", "t0ken", "--admin-token", "adm1n")
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

	// Any machine may join, with no token; it waits until admitted.
	contains(t, "login", r.mustSwim("auth", "login", url, "--access", "read", "--name", "box"), "logged in to "+url+" as swimmer box", "waiting for the hub's admin to admit this machine")
	if w := hubGet(t, url); len(w.Workers) != 0 {
		t.Fatalf("a swimmer waiting to be admitted is listed: %+v", w)
	}
	contains(t, "waiting", r.mustSwim("auth", "admit", "--hub", url, "--admin-token", "adm1n"), "box", "admit one:")
	contains(t, "status", r.mustSwim("auth", "status"), "swimmer: box", "waiting for the hub's admin")
	contains(t, "admit", r.mustSwim("auth", "admit", "box", "--hub", url, "--admin-token", "adm1n"), "box admitted")
	contains(t, "status", r.mustSwim("auth", "status"), "swimmer: box", "state:   admitted")
	// The directory joins as swim watch starts, named by swim.yml.
	r.hubName("billing")
	contains(t, "join", r.mustSwim("watch", "--once"), "joined", "as billing")
	if w := hubGet(t, url); len(w.Workers) != 1 || w.Workers[0].Name != "billing" || len(w.Workers[0].Lanes) != 0 {
		t.Fatalf("after joining: %+v", w)
	}

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
	resp, _ := http.Get(url + "/api/enrollments/" + w.ID)
	sc2 := bufio.NewScanner(resp.Body)
	page := ""
	for sc2.Scan() {
		page += sc2.Text() + "\n"
	}
	resp.Body.Close()
	contains(t, "enrollment", page, `"name":"billing"`, `"activity":"stopped"`)

	contains(t, "logout", r.mustSwim("auth", "logout"), "logged out of "+url, "1 directories")
	if w := hubGet(t, url); len(w.Workers) != 0 {
		t.Errorf("still listed after logout: %+v", w)
	}
	// swim hub serves only.
	if out, code := r.swim("hub", "enroll", url); code != 2 || !strings.Contains(out, "swim auth --help") {
		t.Errorf("swim hub enroll: exit %d\n%s", code, out)
	}
	time.Sleep(10 * time.Millisecond)
}
