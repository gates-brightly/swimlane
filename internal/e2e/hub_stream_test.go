package e2e

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Under hub.logs: stream, a running lane's log reaches the hub as its
// steps end, masked, and is the lane's log byte for byte once it's over.
func TestHubStreamsLogs(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	state := t.TempDir()
	url, stop := startHub(t, r, state, "127.0.0.1:0")
	defer stop()
	r.join(t, url, "billing", "read")
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("secret_env: [STREAM_SECRET]\nhub:\n  logs: stream\n"), 0o644)
	r.script(1, "three steps", `run "one" bash -c 'echo first-$STREAM_SECRET'
run "two" bash -c 'while [ ! -e "$SWIM_ROOT/go" ]; do sleep 0.05; done; echo second'
run "three" bash -c 'echo third'`)
	r.mustSwim("queue", "1")
	w := r.cmd(bin, "watch", "--once", "--interval", "200ms")
	w.Env = append(w.Env, "STREAM_SECRET=hunter2-s3cr3t")
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	streamed := func() string {
		files, _ := filepath.Glob(filepath.Join(state, "runs", "*", "*.lane.1.log"))
		if len(files) != 1 {
			return ""
		}
		b, _ := os.ReadFile(files[0])
		return string(b)
	}
	// Mid-round: step one is there, step three isn't yet.
	waitFor(t, func() bool { s := streamed(); return strings.Contains(s, "first-") && !strings.Contains(s, "third") })
	if s := streamed(); strings.Contains(s, "hunter2-s3cr3t") || strings.Contains(s, "first-\n") || strings.Contains(s, "first- ") {
		t.Fatalf("the secret reached the hub, or the lane never had one to mask:\n%s", s)
	}
	r.release("go")
	if err := w.Wait(); err != nil {
		t.Fatalf("watch: %v", err)
	}
	logData, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent1.log"))
	log := string(logData)
	if i := strings.Index(log, "== ROUND "); i > 0 {
		log = log[i:]
	}
	if strings.Contains(log, "hunter2-s3cr3t") || !strings.Contains(log, "third") {
		t.Fatalf("the lane's own log:\n%s", log)
	}
	waitFor(t, func() bool { return streamed() == log })
}

// Without hub.logs: stream, nothing streams.
func TestHubStreamsNothingByDefault(t *testing.T) {
	long(t)
	for _, cfg := range []string{"", "hub:\n  logs: off\n"} {
		r := newRepo(t, "")
		state := t.TempDir()
		url, stop := startHub(t, r, state, "127.0.0.1:0")
		defer stop()
		r.join(t, url, "billing", "read")
		os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte(cfg), 0o644)
		r.script(1, "one step", `run "one" echo hello`)
		r.mustSwim("queue", "1")
		if out, err := r.cmd(bin, "watch", "--once", "--interval", "200ms").CombinedOutput(); err != nil {
			t.Fatalf("watch: %v\n%s", err, out)
		}
		waitFor(t, func() bool {
			runs, _ := filepath.Glob(filepath.Join(state, "runs", "*", "*.json"))
			return len(runs) == 1
		})
		if logs, _ := filepath.Glob(filepath.Join(state, "runs", "*", "*.lane.*.log")); len(logs) != 0 {
			t.Errorf("swim.yml %q: streamed %v", cfg, logs)
		}
	}
}

// A headless worker's running lane: each step's end reaches an open event
// stream as a log event within a report or two, and the round's end as a
// final one, which is what the app's job view follows.
func TestHubLiveLogEvents(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	url, stop := startHub(t, r, t.TempDir(), "127.0.0.1:0")
	defer stop()
	r.join(t, url, "billing", "read")
	t.Cleanup(func() { r.swim("watch", "--headless", "--stop") })
	r.write("swim.yml", "hub:\n  logs: stream\n", 0o644)
	r.script(1, "three steps", `run "one" echo first
run "two" bash -c 'while [ ! -e "$SWIM_ROOT/go" ]; do sleep 0.05; done; echo second'
run "three" bash -c 'echo third'`)
	resp, err := http.Get(url + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		kind := ""
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "event: "):
				kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && kind == "log":
				events <- strings.TrimPrefix(line, "data: ")
			}
		}
		close(events)
	}()
	r.mustSwim("queue", "1")
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	start := time.Now()
	var first, final string
	for final == "" {
		select {
		case d, ok := <-events:
			if !ok {
				t.Fatal("the event stream ended")
			}
			if !strings.Contains(d, `"lane":1`) {
				continue
			}
			if first == "" {
				first = d
				if s := r.status().Lanes[0].State; s != "running" {
					t.Errorf("the first log event came with swim 1 %s, not running: %s", s, d)
				}
				r.release("go")
			}
			if strings.Contains(d, `"final":true`) {
				final = d
			}
		case <-time.After(waitDeadline):
			t.Fatalf("no final log event in %s (first: %q)", time.Since(start), first)
		}
	}
}
