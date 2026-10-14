package e2e

// Fault injection (tasks.md T7): a swim built with -tags swimfailpoint
// stops at a chosen point (internal/failpoint), the test kills the worker
// there the way a crash or a power cut would, and a restart with the
// normal binary must read what was left correctly: no false success, no
// silent re-run, a repairable chain.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/failpoint"
	"github.com/gates-brightly/swimlane/internal/status"
)

var (
	fpOnce sync.Once
	fpBin  string
	fpErr  error
)

// failpointBin builds swim with the failpoints compiled in, once per test
// binary, next to the plain one TestMain built.
func failpointBin(t *testing.T) string {
	t.Helper()
	fpOnce.Do(func() {
		dir := filepath.Join(filepath.Dir(bin), "fp")
		if fpErr = os.MkdirAll(dir, 0o755); fpErr != nil {
			return
		}
		fpBin = filepath.Join(dir, "swim")
		_, file, _, _ := runtime.Caller(0)
		build := exec.Command("go", "build", "-buildvcs=false", "-tags", "swimfailpoint", "-o", fpBin, "./cmd/swim")
		build.Dir = filepath.Join(filepath.Dir(file), "..", "..")
		if out, err := build.CombinedOutput(); err != nil {
			fpErr = fmt.Errorf("go build -tags swimfailpoint: %v\n%s", err, out)
		}
	})
	if fpErr != nil {
		t.Fatal(fpErr)
	}
	return fpBin
}

// ledgerEnv names the file lanes append their timestamps to, through the
// test binary itself (stampMain): bash 3.2 has no clock finer than a
// second, and the checks compare start and end times across lanes.
const ledgerEnv = "SWIM_E2E_LEDGER"

// stampMain is the test binary run as `<test> stamp LANE WHAT` from a
// lane: it appends "RUN LANE WHAT UNIXNANO" to the ledger in one write
// (O_APPEND: lines from concurrent lanes never mix) and exits.
func stampMain(args []string) {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: stamp LANE WHAT")
		os.Exit(2)
	}
	line := fmt.Sprintf("%s %s %s %d\n", or(os.Getenv("SWIM_RUN"), "-"), args[0], args[1], time.Now().UnixNano())
	f, err := os.OpenFile(os.Getenv(ledgerEnv), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		_, err = f.WriteString(line)
		f.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stamp:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// withLedger gives r's commands a ledger and returns its path, and the
// line a lane script runs to stamp it.
func withLedger(t *testing.T, r *repo) (path string, stamp func(n int, what string) string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "ledger")
	r.env = append(r.env, ledgerEnv+"="+path, "E2E_STAMP="+self)
	return path, func(n int, what string) string {
		return fmt.Sprintf(`"$E2E_STAMP" stamp %d %s || exit 1`, n, what)
	}
}

// stamp is one ledger line.
type stamp struct {
	run  string
	lane int
	what string
	at   int64
}

func readLedger(t *testing.T, path string) []stamp {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []stamp
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(l)
		if len(f) != 4 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		at, _ := strconv.ParseInt(f[3], 10, 64)
		out = append(out, stamp{f[0], n, f[2], at})
	}
	return out
}

// interval is one lane round's time between its start and end stamps.
type interval struct {
	run        string
	lane       int
	start, end int64
}

// intervals pairs each round's start and end stamps (by run and lane); a
// round that started twice in one run is an error.
func intervals(t *testing.T, ls []stamp) []interval {
	t.Helper()
	type key struct {
		run  string
		lane int
	}
	m := map[key]*interval{}
	var order []key
	for _, s := range ls {
		k := key{s.run, s.lane}
		iv := m[k]
		if iv == nil {
			iv = &interval{run: s.run, lane: s.lane}
			m[k] = iv
			order = append(order, k)
		}
		switch s.what {
		case "start":
			if iv.start != 0 {
				t.Errorf("swim %d started twice in run %s", s.lane, s.run)
			}
			iv.start = s.at
		case "end":
			iv.end = s.at
		}
	}
	var out []interval
	for _, k := range order {
		out = append(out, *m[k])
	}
	return out
}

// faultRun starts the failpoint binary with args, SWIM_FAILPOINT=spec and
// a fresh SWIM_FAILPOINT_DIR, in its own process group. It returns the
// command (started) and the directory.
func faultRun(t *testing.T, r *repo, spec string, args ...string) (*exec.Cmd, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	c := r.cmd(failpointBin(t), args...)
	c.Env = append(c.Env, failpoint.EnvVar+"="+spec, failpoint.DirEnv+"="+dir)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-c.Process.Pid, syscall.SIGKILL) })
	return c, &out, dir
}

// waitHit waits for a process to reach site (it wrote DIR/<site>.hit) and
// returns that process's pid. A loaded machine may take a while.
func waitHit(t *testing.T, dir, site string, out *bytes.Buffer) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(dir, site+".hit"))
		if err == nil && strings.HasSuffix(string(data), "\n") {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no process reached failpoint %s within 60s:\n%s", site, out)
	return 0
}

// crash kills the whole worker as a power cut would, with no traps run:
// the command's process group, every lane's group, and the process paused
// at the failpoint. It waits for the command to be gone.
func crash(r *repo, c *exec.Cmd, paused int) {
	syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	killLanes(r.root)
	if paused > 0 {
		syscall.Kill(paused, syscall.SIGKILL)
	}
	c.Wait()
}

// A crash after a step's command exited but before swim step wrote its
// result: the restart (swim watch) ends the round as interrupted with the
// step recovered as cut off, never as passed, and runs nothing again; the
// non-idempotent step ran exactly once. Rerunning it takes an explicit
// swim run.
func TestFaultStepAfterExit(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "charge", `run "charge" bash -c 'echo charged >> charges'
run "after" touch after`)
	c, out, dir := faultRun(t, r, failpoint.StepAfterExit+":pause", "run", "1")
	pid := waitHit(t, dir, failpoint.StepAfterExit, out)
	crash(r, c, pid)

	if l := r.status().Get(1); l.State != status.Running {
		t.Fatalf("after the crash: state %q, want running until reaped", l.State)
	}
	restart := r.mustSwim("watch", "--once")
	contains(t, "restart", restart, "swim 1: its process died without ending its round")
	l := r.status().Get(1)
	if l.State != status.Interrupted || l.ExitCode == nil || *l.ExitCode != 137 {
		t.Fatalf("after the restart: %+v", l)
	}
	log := r.log(1)
	contains(t, "log", log, "FAIL  charge (cut off: swim step was killed, output recovered)", "== END INTERRUPTED")
	if strings.Contains(log, "PASS  charge") {
		t.Errorf("the cut-off step reads as passed:\n%s", log)
	}
	if data, _ := os.ReadFile(filepath.Join(r.root, "charges")); string(data) != "charged\n" {
		t.Errorf("the step ran %q, want exactly once", data)
	}
	if _, err := os.Stat(filepath.Join(r.root, "after")); err == nil {
		t.Error("the step after the crash ran")
	}
	links, _ := chain.Read(r.root)
	if last := links[len(links)-1]; last.Type != "round" || last.Result != "interrupted" {
		t.Errorf("chain's last entry: %+v", last.Entry)
	}
	// Restarting again changes nothing: the round is closed, nothing reruns.
	if out := r.mustSwim("watch", "--once"); strings.Contains(out, "died") {
		t.Errorf("reaped twice:\n%s", out)
	}
	if strings.Count(r.log(1), "== ROUND ") != 1 {
		t.Errorf("a restart started a new round:\n%s", r.log(1))
	}
}

// A worker killed right after it renamed an entry to .running, before it
// recorded itself: the entry names no worker, so it is orphaned, and a new
// watch doesn't run it. It runs again only when requeued.
func TestFaultQueueClaimed(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "charge", `run "charge" bash -c 'echo charged >> charges'`)
	r.mustSwim("queue", "add", "1")
	c, out, dir := faultRun(t, r, failpoint.QueueClaimed+":kill", "watch", "--once", "--no-tui")
	waitHit(t, dir, failpoint.QueueClaimed, out)
	crash(r, c, 0)

	contains(t, "queue", r.mustSwim("queue"), "orphaned: its worker is gone")
	r.mustSwim("watch", "--once")
	if _, err := os.Stat(filepath.Join(r.root, "charges")); err == nil {
		t.Fatal("a new watch ran the orphaned entry")
	}
	contains(t, "queue after a new watch", r.mustSwim("queue"), "orphaned")
	r.mustSwim("queue", "requeue", "1")
	r.mustSwim("watch", "--once")
	if data, _ := os.ReadFile(filepath.Join(r.root, "charges")); string(data) != "charged\n" {
		t.Errorf("after requeue: ran %q, want once", data)
	}
}

// A watch killed in the middle of a batch: its claim's worker is gone (the
// pid and start time it recorded), so the entry is orphaned; the next
// watch ends the dead lane's round and doesn't rerun the batch.
func TestFaultWatchMidBatch(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "charge", `run "charge" bash -c 'echo charged >> charges'
run "after" touch after`)
	r.mustSwim("queue", "add", "1")
	c, out, dir := faultRun(t, r, failpoint.StepAfterExit+":pause", "watch", "--once", "--no-tui")
	pid := waitHit(t, dir, failpoint.StepAfterExit, out)
	crash(r, c, pid)

	contains(t, "queue", r.mustSwim("queue"), "orphaned: its worker is gone")
	restart := r.mustSwim("watch", "--once")
	contains(t, "restart", restart, "swim 1: its process died without ending its round")
	if l := r.status().Get(1); l.State != status.Interrupted {
		t.Errorf("after the restart: state %q", l.State)
	}
	if data, _ := os.ReadFile(filepath.Join(r.root, "charges")); string(data) != "charged\n" {
		t.Errorf("the step ran %q, want exactly once", data)
	}
	if n := len(doneEntries(t, r)); n != 0 {
		t.Errorf("the orphaned batch was consumed (%d done)", n)
	}
	contains(t, "queue after the restart", r.mustSwim("queue"), "orphaned")
}

// A crash halfway through writing status.yml's replacement leaves the
// last complete file in place: every later reader parses it, and the next
// run goes on. Crashed at several of the run's writes in turn.
func TestFaultStatusMidWrite(t *testing.T) {
	for _, nth := range []int{1, 2, 4, 7} {
		t.Run(fmt.Sprint("write ", nth), func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, "")
			r.script(1, "one", `run "a" true
run "b" true`)
			r.mustSwim("run", "1")
			c, out, dir := faultRun(t, r, fmt.Sprintf("%s:pause@%d", failpoint.StatusMidWrite, nth), "run", "1")
			pid := waitHit(t, dir, failpoint.StatusMidWrite, out)
			crash(r, c, pid)

			// The half-written replacement is left beside the file, never
			// in its place: status.yml is one a writer finished.
			if tmps, _ := filepath.Glob(filepath.Join(status.Dir(r.root), ".status.yml.*.tmp")); len(tmps) != 1 {
				t.Errorf("want the half-written temporary file left behind: %v", tmps)
			}
			f, err := status.Load(r.root)
			if err != nil {
				t.Fatalf("status.yml after the crash: %v", err)
			}
			if st := f.Get(1).State; st == "" {
				t.Errorf("status.yml after the crash has no state for swim 1")
			}
			r.mustSwim("status")
			r.mustSwim("run", "1")
			if l := r.status().Get(1); l.State != status.Passed {
				t.Errorf("the next run: state %q", l.State)
			}
		})
	}
}

// A crash halfway through a chain append (half the line on disk, synced):
// the next append and swim verify refuse the incomplete entry; swim chain
// repair cuts it off and records the repair; then appends and verify work.
func TestFaultChainMidAppend(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "a" true`)
	r.mustSwim("run", "1")
	c, out, dir := faultRun(t, r, failpoint.ChainMidAppend+":pause", "run", "1")
	pid := waitHit(t, dir, failpoint.ChainMidAppend, out)
	crash(r, c, pid)

	if _, err := chain.Read(r.root); err == nil || !strings.Contains(err.Error(), "incomplete entry") {
		t.Fatalf("reading the chain after the crash: %v", err)
	}
	vout, code := r.swim("verify")
	if code == 0 {
		t.Fatalf("verify passed a chain cut mid-append:\n%s", vout)
	}
	contains(t, "verify", vout, "an incomplete entry", "swim chain repair removes it")
	// The next append refuses rather than link to a broken line.
	contains(t, "run before repair", must(r.swim("run", "1")), "the last entry is incomplete")
	contains(t, "repair", r.mustSwim("chain", "repair"), "removed", "of an incomplete final entry")
	contains(t, "verify after repair", r.mustSwim("verify"), "consistent  yes")
	r.mustSwim("run", "1")
	contains(t, "verify after the next run", r.mustSwim("verify"), "consistent  yes")
	links, err := chain.Read(r.root)
	if err != nil {
		t.Fatal(err)
	}
	repairs, roundAfter := 0, false
	for _, l := range links {
		switch {
		case l.Type == "repair":
			repairs++
		case l.Type == "round" && repairs > 0:
			roundAfter = true
		}
	}
	if repairs != 1 || !roundAfter {
		t.Errorf("want one repair entry, then the next round: %d repairs, a round after it: %v", repairs, roundAfter)
	}
}

// Several real swim watch processes on one repo: every batch runs exactly
// once (each run's lanes stamp a shared ledger), and lanes sharing a lock
// never overlap, within a batch or across them.
func TestFaultManyWatchers(t *testing.T) {
	const watchers, batches = 4, 20
	r := newRepo(t, "")
	ledger, stampLine := withLedger(t, r)
	for n := 1; n <= 3; n++ {
		r.script(n, "work", stampLine(n, "start")+`
run "work" sleep 0.05
`+stampLine(n, "end"))
	}
	withLocks(t, r, 1, "db")
	withLocks(t, r, 2, "db")
	for i := 0; i < batches; i++ {
		r.mustSwim("queue", "add", "1", "2", "3")
	}
	var wg sync.WaitGroup
	outs := make([]string, watchers)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, _ := r.cmd(bin, "watch", "--once", "--no-tui", "--interval", "100ms").CombinedOutput()
			outs[i] = string(out)
		}(i)
	}
	wg.Wait()

	done := doneEntries(t, r)
	if len(done) != batches {
		t.Fatalf("consumed %d of %d batches\n%s", len(done), batches, strings.Join(outs, "\n---\n"))
	}
	runs := map[string]bool{}
	for _, e := range done {
		if e.Result == nil || e.Result.Result != "passed" || runs[e.Result.Run] {
			t.Errorf("entry %s: %+v", e.ID, e.Result)
			continue
		}
		runs[e.Result.Run] = true
	}
	ivs := intervals(t, readLedger(t, ledger))
	per := map[string]int{}
	for _, iv := range ivs {
		if !runs[iv.run] {
			t.Errorf("swim %d ran in %s, which no consumed batch names", iv.lane, iv.run)
		}
		if iv.start == 0 || iv.end == 0 {
			t.Errorf("swim %d in %s: start or end missing", iv.lane, iv.run)
		}
		per[iv.run]++
	}
	for run := range runs {
		if per[run] != 3 {
			t.Errorf("run %s: %d lanes ran, want 3", run, per[run])
		}
	}
	if len(ivs) != 3*batches {
		t.Errorf("%d lane rounds in the ledger, want %d", len(ivs), 3*batches)
	}
	noOverlap(t, ivs, func(a, b int) bool { return a != 3 && b != 3 })
}

// noOverlap fails for any two rounds whose lanes share a lock (shares)
// that ran at the same time.
func noOverlap(t *testing.T, ivs []interval, shares func(a, b int) bool) {
	t.Helper()
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start < ivs[j].start })
	for i, a := range ivs {
		for _, b := range ivs[i+1:] {
			if a.lane == b.lane || !shares(a.lane, b.lane) {
				continue
			}
			if b.start < a.end && a.start < b.end {
				t.Errorf("swim %d (%s) and swim %d (%s) share a lock but overlapped", a.lane, a.run, b.lane, b.run)
			}
		}
	}
}
