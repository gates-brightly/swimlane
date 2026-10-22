package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Under hub.logs: stream, a running lane's log reaches the hub as its
// steps end, masked, and is the lane's log byte for byte once it's over.
func TestHubStreamsLogs(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	state := t.TempDir()
	url, stop := startHub(t, r, state, "127.0.0.1:0")
	defer stop()
	r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "billing")
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("secret_env: [STREAM_SECRET]\nhub:\n  logs: stream\n"), 0o644)
	r.script(1, "three steps", `run "one" bash -c 'echo first-$STREAM_SECRET'
run "two" bash -c 'sleep 1.5; echo second'
run "three" bash -c 'sleep 1.5; echo third'`)
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
		r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "billing")
		os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte(cfg), 0o644)
		r.script(1, "one step", `run "one" echo hello`)
		r.mustSwim("queue", "1")
		if out, err := r.cmd(bin, "watch", "--once", "--interval", "200ms").CombinedOutput(); err != nil {
			t.Fatalf("watch: %v\n%s", err, out)
		}
		waitFor(t, func() bool { runs, _ := filepath.Glob(filepath.Join(state, "runs", "*", "*.json")); return len(runs) == 1 })
		if logs, _ := filepath.Glob(filepath.Join(state, "runs", "*", "*.lane.*.log")); len(logs) != 0 {
			t.Errorf("swim.yml %q: streamed %v", cfg, logs)
		}
	}
}
