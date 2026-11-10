package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

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
	contains(t, "queue", list, "#1", "queued", "left queued: the watcher, swim 0.20261001.1, can't run it",
		"lane 1's script declares syntax 4, and it reads up to 3", "queued by swim 0.20261001.1")
	if len(doneEntries(t, r)) != 0 {
		t.Errorf("the batch was consumed:\n%s\n%s", list, out.String())
	}
	contains(t, "watch", out.String(), "#1 left queued")
	contains(t, ".swim.log", r.mustSwim("log"), "queue-kept", "#1 q-", "the watcher, swim 0.20261001.1, can't run it")
	contains(t, "queue show", r.mustSwim("queue", "show", "#1"), "# left queued: the watcher, swim 0.20261001.1, can't run it")

	// A new batch is refused at queue add, naming both swims.
	msg, code := r.swim("queue", "add", "1")
	if code == 0 {
		t.Fatalf("queue add with only a worker that can't run it:\n%s", msg)
	}
	contains(t, "queue add", msg, "no watcher running now can run this batch",
		"pid", "swim 0.20261001.1): lane 1's script declares syntax 4, and it reads up to 3",
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

// The watcher says what platform its lanes run on (OS, architecture, its
// bash): a watcher on another host shows in swim queue and swim status,
// and their --yaml; with none up, the last one seen does, marked so. A
// real swim watch records its own.
func TestWatcherPlatform(t *testing.T) {
	r := newRepo(t)
	r.script(1, "one", `step "a" true`)
	far := func(beat time.Time) string {
		ts := beat.UTC().Format(time.RFC3339)
		return "host: far-away\npid: 4242\nuser: zach\nclaimed: \"" + ts + "\"\nheartbeat: \"" + ts +
			"\"\nlease: 2m\nversion: 0.20261110.1\nsyntax: 4\n" +
			"platform:\n  os: darwin\n  arch: arm64\n  bash: 3.2.57(1)-release\n"
	}
	presence := filepath.Join(r.root, ".swim", "workers", "far-away-4242.yml")
	os.MkdirAll(filepath.Dir(presence), 0o755)
	if err := os.WriteFile(presence, []byte(far(time.Now())), 0o644); err != nil {
		t.Fatal(err)
	}
	r.mustSwim("queue", "add", "1")
	const line = "watcher: far-away, darwin/arm64, bash 3.2"
	contains(t, "queue", r.mustSwim("queue"), line)
	contains(t, "status", r.mustSwim("status"), "queue: 1 queued", line)
	for _, args := range [][]string{{"queue", "--yaml"}, {"status", "--yaml"}} {
		contains(t, strings.Join(args, " "), r.mustSwim(args...), "host: far-away", "os: darwin", "arch: arm64", "bash: 3.2.57(1)-release", "up: true")
	}

	// None up: the last one seen, marked as such.
	os.Remove(presence)
	os.WriteFile(filepath.Join(r.root, ".swim", "watcher.yml"), []byte(far(time.Now().Add(-3*time.Hour))), 0o644)
	contains(t, "queue, none up", r.mustSwim("queue"), "watcher: none up; last seen far-away, darwin/arm64, bash 3.2, 3h ago")
	contains(t, "status --yaml, none up", r.mustSwim("status", "--yaml"), "host: far-away", "up: false")

	// A real watcher records this machine's platform, and is the last seen
	// once it stops.
	r.mustSwim("watch", "--once", "--plain")
	contains(t, "after swim watch", r.mustSwim("queue", "--yaml"),
		"host: "+hostname(), "os: "+runtime.GOOS, "arch: "+runtime.GOARCH, "bash: ", "up: false")
}

func mustRun(t *testing.T, r *repo, swim string, args ...string) string {
	t.Helper()
	out, err := r.cmd(swim, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(swim), args, err, out)
	}
	return string(out)
}
