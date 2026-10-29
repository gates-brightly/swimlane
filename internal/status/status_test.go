package status

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeFillsAllLanes(t *testing.T) {
	f := &File{Lanes: []Lane{{Lane: 3, State: Passed}}}
	f.Normalize(4)
	if len(f.Lanes) != 4 {
		t.Fatalf("lanes = %d", len(f.Lanes))
	}
	for i, l := range f.Lanes {
		if l.Lane != i+1 {
			t.Fatalf("lane order: %+v", f.Lanes)
		}
	}
	if f.Get(3).State != Passed || f.Get(1).State != Idle || f.Get(2).Script != "lane.2.sh" {
		t.Fatalf("unexpected: %+v", f.Lanes)
	}
}

func TestConcurrentUpdatesLoseNothing(t *testing.T) {
	root := t.TempDir()
	const workers, each = 8, 25
	var wg sync.WaitGroup
	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go func(lane int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := Update(root, lane, workers, func(l *Lane) {
					l.Pass++
					l.FailedSteps = append(l.FailedSteps, fmt.Sprintf("s%d", i))
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	f, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range f.Lanes {
		if l.Pass != each || len(l.FailedSteps) != each {
			t.Errorf("lane %d: pass=%d steps=%d", l.Lane, l.Pass, len(l.FailedSteps))
		}
	}
}

// TestConcurrentProcesses re-execs the test binary so updates race across
// processes, which is how parallel lanes actually write the file.
func TestConcurrentProcesses(t *testing.T) {
	if root := os.Getenv("SWIM_STATUS_CHILD"); root != "" {
		lane := 0
		fmt.Sscan(os.Getenv("SWIM_STATUS_LANE"), &lane)
		for i := 0; i < 20; i++ {
			if err := Update(root, lane, 4, func(l *Lane) { l.Pass++ }); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	root := t.TempDir()
	var cmds []*exec.Cmd
	for lane := 1; lane <= 4; lane++ {
		c := exec.Command(os.Args[0], "-test.run=TestConcurrentProcesses")
		c.Env = append(os.Environ(), "SWIM_STATUS_CHILD="+root, fmt.Sprintf("SWIM_STATUS_LANE=%d", lane))
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, c)
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	f, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range f.Lanes {
		if l.Pass != 20 {
			t.Errorf("lane %d pass=%d", l.Lane, l.Pass)
		}
	}
}

func TestWrittenFileShape(t *testing.T) {
	root := t.TempDir()
	if err := Update(root, 2, 2, func(l *Lane) {
		l.State = Running
		l.StartedAt = Str("2026-10-09T12:00:00Z")
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Path(root))
	s := string(data)
	for _, want := range []string{`"lane": 1`, `"state": "idle"`, `"state": "running"`, `"finished_at": null`, `"waiting_on": []`, `"script": "lane.2.sh"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// It's JSON, and still YAML to whatever reads it as YAML.
	var y File
	if err := yaml.Unmarshal(data, &y); err != nil || len(y.Lanes) != 2 || y.Lanes[1].State != Running || *y.Lanes[1].StartedAt != "2026-10-09T12:00:00Z" {
		t.Errorf("status.yml as YAML: %v %+v", err, y.Lanes)
	}
}

// A status.yml an older swim wrote (YAML) still reads, and the next
// update writes it as JSON.
func TestReadsOlderYAML(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(Dir(root), 0o755)
	os.WriteFile(Path(root), []byte("# swim status\nupdated_at: \"2026-10-01T00:00:00Z\"\nroot: /x\nlanes:\n  - lane: 1\n    state: passed\n    job: j1\n"), 0o644)
	f, err := Load(root)
	if err != nil || f.Get(1) == nil || f.Get(1).State != Passed || f.Get(1).Job != "j1" {
		t.Fatalf("older YAML: %v %+v", err, f)
	}
	if err := Update(root, 1, 1, func(l *Lane) { l.Round = "next" }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Path(root))
	if !strings.HasPrefix(string(data), "{") || !strings.Contains(string(data), `"job": "j1"`) {
		t.Errorf("after an update:\n%s", data)
	}
}

// Guards approved without a prompt are listed once each, written as
// auto_approved / flag_approved, and cleared when the next round starts.
func TestUnprompted(t *testing.T) {
	root := t.TempDir()
	err := Update(root, 1, 1, func(l *Lane) {
		l.Unprompted("DEPLOY", "swim run -y")
		l.Unprompted("DEPLOY", "swim run -y")
		l.Unprompted("SWAP", "swim run -y")
		l.Unprompted("SEED", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := Load(root)
	l := f.Get(1)
	if strings.Join(l.AutoApproved, ",") != "DEPLOY,SWAP" || l.AutoVia != "swim run -y" || strings.Join(l.FlagApproved, ",") != "SEED" {
		t.Errorf("auto %v via %q flag %v", l.AutoApproved, l.AutoVia, l.FlagApproved)
	}
	if got := l.UnpromptedText(); got != "DEPLOY, SWAP (swim run -y); SEED (flag set)" {
		t.Errorf("text: %q", got)
	}
	l.ResetRun()
	if l.UnpromptedText() != "" || l.AutoVia != "" {
		t.Errorf("a new round keeps the last one's: %+v", l)
	}
}
