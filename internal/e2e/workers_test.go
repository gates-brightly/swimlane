package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/version"
)

// oldSwim builds a swim that reads up to syntax 3 (version.TestSyntax),
// tagged as an older release: a worker of another build.
func oldSwim(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "swim")
	const v = "github.com/gates-brightly/swimlane/internal/version"
	build := exec.Command("go", "build", "-buildvcs=false", "-o", path,
		"-ldflags", "-X "+v+".TestSyntax=3 -X "+v+".BuildTag=v0.20261001.1", "./cmd/swim")
	build.Dir = repoRoot()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	return path
}

// A worker whose swim can't run a batch: a lane's script was moved to a
// newer syntax under it. swim queue add refuses, naming both swims; a
// batch that reaches it anyway stays queued, with why, instead of failing.
func TestQueueRefusesBatchTheWorkerCantRun(t *testing.T) {
	old := oldSwim(t)
	r := newRepo(t)
	r.script(1, "probe", `step "probe" true`) // syntax 2: the old swim reads it

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
	// stands in for it, so the worker can't start the batch yet), then the
	// script moves to syntax 4 under the worker.
	setLane1 := func(state string, pid int) {
		status.UpdateFile(r.root, 1, func(f *status.File) error {
			l := f.Get(1)
			l.State, l.PID = state, pid
			return nil
		})
	}
	setLane1(status.Waiting, os.Getpid())
	mustRun(t, r, old, "queue", "add", "1")
	script := filepath.Join(r.root, "lane.1.sh")
	data, _ := os.ReadFile(script)
	os.WriteFile(script, bytes.Replace(data, []byte("# swim: syntax 2"), []byte("# swim: syntax 4"), 1), 0o755)
	setLane1(status.Skipped, 0) // its stand-in run ends it

	// The worker leaves it queued, and says why.
	waitFor(t, func() bool { return strings.Contains(r.mustSwim("queue"), "left queued") })
	// Its answer (cantRunHere) depends only on the scripts, which don't
	// change from here: a later pass says the same, so there's
	// nothing more to wait for.
	list := r.mustSwim("queue")
	contains(t, "queue", list, "#1", "queued", "left queued: this swimmer, swim 0.20261001.1, can't run it",
		"swim 1's script declares syntax 4, and it reads up to 3", "queued by swim 0.20261001.1")
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
		"pid", "swim 0.20261001.1): swim 1's script declares syntax 4, and it reads up to 3",
		"this swim is "+version.String()+" (syntax 4)", "restart its swim watch")

	// Once the worker is gone, queue add queues and warns.
	syscall.Kill(-w.Process.Pid, syscall.SIGINT)
	w.Wait()
	msg, code = r.swim("queue", "add", "1")
	if code != 0 {
		t.Fatalf("queue add with no worker: exit %d\n%s", code, msg)
	}
	contains(t, "no worker", msg, "no swim watch is running yet: it waits for one")
}

func mustRun(t *testing.T, r *repo, swim string, args ...string) string {
	t.Helper()
	out, err := r.cmd(swim, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(swim), args, err, out)
	}
	return string(out)
}
