package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
)

func queued(t *testing.T, root string, n int, job string) *queue.Entry {
	t.Helper()
	e := &queue.Entry{By: "test", Lanes: []queue.Lane{{Lane: n, Job: job, Round: "r"}}}
	if err := queue.Add(root, e); err != nil {
		t.Fatal(err)
	}
	return e
}

func finish(t *testing.T, root string, e *queue.Entry, result string) {
	t.Helper()
	c, err := queue.Claim(root, e, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Consume(root, c, queue.Result{Result: result}); err != nil {
		t.Fatal(err)
	}
}

func exitCode(err error) int {
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if err != nil {
		return -1
	}
	return 0
}

func batches(root string, refs ...string) []waitTarget {
	var ts []waitTarget
	for _, r := range refs {
		ts = append(ts, batchTarget(root, r))
	}
	return ts
}

// swim wait BATCH|JOB ... waits for those batches alone, not the whole
// repo; --any returns when the first ends, with its result.
func TestWaitBatches(t *testing.T) {
	root := t.TempDir()
	a := queued(t, root, 30, "aaaaaaaa-1111")
	b := queued(t, root, 31, "bbbbbbbb-2222")
	queued(t, root, 19, "cccccccc-3333") // another session's: never waited on

	// Neither has ended: a short limit gives up, naming what it waits on.
	err := waitFor(batches(root, a.ID, "bbbbbbbb"), true, 10*time.Millisecond, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "#1 queued") || !strings.Contains(err.Error(), "#2 queued") {
		t.Fatalf("before either ended: %v", err)
	}

	// --any: b passes first, so it returns at once, passed.
	finish(t, root, b, status.Passed)
	if code := exitCode(waitFor(batches(root, a.ID, b.ID), true, 10*time.Millisecond, time.Second)); code != 0 {
		t.Fatalf("--any after b passed: exit %d", code)
	}
	// Without --any it still waits on a.
	if err := waitFor(batches(root, a.ID, b.ID), false, 10*time.Millisecond, 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "#1 queued") {
		t.Fatalf("all, with a queued: %v", err)
	}
	// A job id finds the batch that queued it; a dry run exits as swim run's does.
	finish(t, root, a, status.Skipped)
	if code := exitCode(waitFor(batches(root, "aaaaaaaa-1111", b.ID), false, 10*time.Millisecond, time.Second)); code != launcher.ExitDryRun {
		t.Fatalf("all, one dry run: exit %d", code)
	}
	if _, err := findBatch(root, "zzzzzzzz"); err == nil {
		t.Fatal("an unknown ref was found")
	}
}
