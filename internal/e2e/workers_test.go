package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/version"
)

// oldSwim builds a swim that reads script syntax 3 (version.TestSyntaxes),
// tagged as an older release: a worker of another build.
func oldSwim(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swim")
	const v = "github.com/gates-brightly/swimlane/internal/version"
	build := exec.Command("go", "build", "-buildvcs=false", "-o", path,
		"-ldflags", "-X "+v+".TestSyntaxes=3,2,2 -X "+v+".BuildTag=v0.20261001.1", "./cmd/swim")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	return path
}

// A worker whose swim can't run a batch: the lock was upgraded under it.
// swim queue add refuses, naming both swims; a batch that reaches it
// anyway stays queued, with why, instead of failing.
func TestQueueRefusesBatchTheWorkerCantRun(t *testing.T) {
	old := oldSwim(t)
	r := newRepo(t, "")
	r.script(1, "probe", `run "probe" true`)
	os.Remove(filepath.Join(r.root, ".swim.lock")) // the old worker writes its own

	w := r.cmd(old, "watch", "--interval", "100ms")
	var out bytes.Buffer
	w.Stdout, w.Stderr = &out, &out
	w.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Process.Kill()
	waitFor(t, func() bool {
		files, _ := filepath.Glob(filepath.Join(r.root, ".swim", "workers", "*.yml"))
		return len(files) == 1
	})

	// Queued while lane 1 waits in another swim run (this test's process
	// stands in for it, so the worker can't start the batch yet, and swim
	// lock, which refuses only over a running lane, can upgrade), then the
	// lock is upgraded under the worker (what happened: swim lock
	// --upgrade with a worker of the older build up).
	setLane1 := func(state string, pid int) {
		status.UpdateFile(r.root, 1, func(f *status.File) error {
			l := f.Get(1)
			l.State, l.PID = state, pid
			return nil
		})
	}
	setLane1(status.Waiting, os.Getpid())
	mustRun(t, r, old, "queue", "add", "1")
	up, _ := r.swim("lock", "--upgrade")
	contains(t, "lock --upgrade", up, "syntax script 3, log 2, state 2 -> "+version.Current.String(),
		"warning: swim watch on", "swim 0.20261001.1) reads script 3, log 2, state 2: it leaves batches queued")
	setLane1(status.Skipped, 0) // its stand-in run ends it

	// The worker leaves it queued, and says why.
	waitFor(t, func() bool { return strings.Contains(r.mustSwim("queue"), "left queued") })
	time.Sleep(300 * time.Millisecond) // a few more polls: still not run
	list := r.mustSwim("queue")
	contains(t, "queue", list, "#1", "queued", "left queued: this swimmer, swim 0.20261001.1, can't run it",
		"it reads script 3, log 2, state 2, and .swim.lock pins "+version.Current.String(), "queued by swim 0.20261001.1, reading script 3, log 2, state 2")
	if len(doneEntries(t, r)) != 0 {
		t.Errorf("the batch was consumed:\n%s\n%s", list, out.String())
	}
	contains(t, "watch", out.String(), "#1 left queued")
	contains(t, ".swim.log", r.mustSwim("log"), "queue-held", "#1 q-", "this swimmer, swim 0.20261001.1, can't run it")
	contains(t, "queue show", r.mustSwim("queue", "show", "#1"), "# left queued: this swimmer, swim 0.20261001.1, can't run it")

	// A new batch is refused at queue add, naming both swims.
	msg, code := r.swim("queue", "add", "1")
	if code == 0 {
		t.Fatalf("queue add with only a worker that can't run it:\n%s", msg)
	}
	contains(t, "queue add", msg, "no swimmer running now can run this batch",
		"pid", "swim 0.20261001.1): it reads script 3, log 2, state 2, and .swim.lock pins "+version.Current.String(),
		"this swim is "+version.String()+" ("+version.Current.String()+")", "restart its swim watch")

	// Once the worker is gone, queue add queues and warns.
	syscall.Kill(-w.Process.Pid, syscall.SIGINT)
	w.Wait()
	msg, code = r.swim("queue", "add", "1")
	if code != 0 {
		t.Fatalf("queue add with no worker: exit %d\n%s", code, msg)
	}
	contains(t, "no worker", msg, "no swimmer is running: the batch waits for a swim watch that reads "+version.Current.String())
}

func mustRun(t *testing.T, r *repo, swim string, args ...string) string {
	t.Helper()
	out, err := r.cmd(swim, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(swim), args, err, out)
	}
	return string(out)
}
