package e2e

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// A run swim watch carries out reaches the hub as a whole: the run record
// and each lane's round with its steps and output, kept in the hub's state.
func TestHubKeepsRuns(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	state := t.TempDir()
	hub := exec.Command(bin, "hub", "--addr", "127.0.0.1:0", "--token", "t0ken", "--state", state, "--admin-token", "adm1n", "--no-tui")
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
	r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "billing")
	r.admit(t, url, "billing")
	r.script(1, "greet", `run "say hello" echo hello-from-lane-1`)
	r.script(2, "count", `run "count" bash -c 'echo one; echo two'`)
	seen := map[string]bool{}
	batch := func(want int) hubRun {
		t.Helper()
		r.mustSwim("queue", "1", "2")
		if out, err := r.cmd(bin, "watch", "--once", "--interval", "200ms").CombinedOutput(); err != nil {
			t.Fatalf("watch: %v\n%s", err, out)
		}
		var files []string
		waitFor(t, func() bool {
			files, _ = filepath.Glob(filepath.Join(state, "runs", "*", "*.json"))
			return len(files) == want
		})
		var file string // the new one (two runs can start in one second)
		for _, f := range files {
			if !seen[f] {
				file, seen[f] = f, true
			}
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var rep hubRun
		if err := json.Unmarshal(data, &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.Run.Lanes) != 2 || len(rep.Jobs) != 2 || rep.Run.Run+".json" != filepath.Base(file) {
			t.Fatalf("kept run: %s", data)
		}
		for _, j := range rep.Jobs {
			if j.Result != "passed" {
				t.Errorf("swim %d: %s", j.Lane, j.Result)
			}
		}
		return rep
	}

	// By default a run goes with its steps, not their output.
	rep := batch(1)
	contains(t, "steps", rep.steps(), "say hello: \n", "count: \n")
	if strings.Contains(rep.steps(), "hello-from-lane-1") {
		t.Errorf("output left the machine without hub.logs: stream:\n%s", rep.steps())
	}
	// hub.logs: stream sends it too.
	os.WriteFile(filepath.Join(r.root, "swim.yml"), []byte("hub:\n  logs: stream\n"), 0o644)
	rep = batch(2)
	contains(t, "steps", rep.steps(), "say hello: hello-from-lane-1", "count: one|two")
	// The worker remembers what the hub took.
	// The read API lists them, newest first, with the run's result.
	id := filepath.Base(filepath.Dir(seenFile(seen)))
	resp, err := http.Get(url + "/api/enrollments/" + id + "/runs")
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Runs []struct{ Run, Result string }
	}
	json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if len(page.Runs) != 2 || page.Runs[0].Run != rep.Run.Run || page.Runs[0].Result != "passed" {
		t.Errorf("GET .../runs: %+v (newest %s)", page, rep.Run.Run)
	}
	sent, _ := os.ReadFile(filepath.Join(r.root, ".swim", "hub-runs.yml"))
	contains(t, "hub-runs.yml", string(sent), rep.Run.Run)
}

type hubRun struct {
	Run struct {
		Run   string
		Lanes []struct {
			Lane  int
			State string
		}
	}
	Jobs []struct {
		Lane    int
		Result  string
		Results []struct {
			Label  string
			Output []string
		}
	}
}

// steps are a kept run's steps, "label: output|output" a line.
func (h hubRun) steps() string {
	all := ""
	for _, j := range h.Jobs {
		for _, res := range j.Results {
			all += res.Label + ": " + strings.Join(res.Output, "|") + "\n"
		}
	}
	return all
}

// seenFile is any one of the kept runs' files.
func seenFile(seen map[string]bool) string {
	for f := range seen {
		return f
	}
	return ""
}
