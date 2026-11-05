package e2e

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// oscRe matches an OSC sequence (ended by BEL), to tell a bell from one's end.
var oscRe = regexp.MustCompile("\x1b\\][^\x07\x1b]*\x07")

// onBranch checks r out on branch (unborn: the repo has no commits) and
// returns what its notification titles start with: the repo's name (its
// directory's: no origin), and the branch unless it's main.
func onBranch(t *testing.T, r *repo, branch string) string {
	t.Helper()
	if out, err := exec.Command("git", "-C", r.root, "symbolic-ref", "HEAD", "refs/heads/"+branch).CombinedOutput(); err != nil {
		t.Fatalf("git symbolic-ref: %v %s", err, out)
	}
	from := filepath.Base(r.root)
	if branch != "main" {
		from += "@" + branch
	}
	return from + " · "
}

// lastProgress is the last progress-bar sequence (OSC 9;4) in out.
func lastProgress(out string) string {
	m := regexp.MustCompile("\x1b\\]9;4;[0-9;]*\x07").FindAllString(out, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// With notify on, swim watch tells the terminal (OSC 777 notifications,
// OSC 9;4 progress, as Ghostty reads them) when a batch's lane waits on the
// operator and when each batch ends, keeps a progress bar while batches run,
// paused while a question waits, and leaves none behind. The bell it rang
// for a question is gone. Each title starts with the repo, without the
// branch on main.
func TestWatchNotifies(t *testing.T) {
	r := newSerialRepo(t)
	from := onBranch(t, r, "main")
	r.script(1, "one", `if guard ALLOW_ONE "change one"; then run "one changed" true; fi`)
	r.script(2, "two", `run "two" true`)
	r.mustSwim("config", "notify", "true")
	contains(t, "config", r.mustSwim("config", "notify"), "true")
	r.mustSwim("queue", "add", "2")
	r.mustSwim("queue", "add", "1")
	tr := r.startTUI(nil, "watch", "--batches", "2")
	tr.waitFor("⚠ swim 1 is blocked")
	tr.waitScreen("the notifications", func(string) bool {
		out := tr.output()
		return strings.Contains(out, "\x1b]777;notify;"+from+"lane 1 needs you;approval of ALLOW_ONE: change one · batch #2\x07") &&
			strings.Contains(out, "\x1b]777;notify;"+from+"batch #1 passed;swim 2 · ")
	})
	tr.waitScreen("the bar paused on the question", func(string) bool { return strings.Contains(tr.output(), "\x1b]9;4;4;") })
	if strings.Contains(oscRe.ReplaceAllString(tr.output(), ""), "\a") {
		t.Error("the bell still rings for a question")
	}
	tr.send("y")
	tr.waitScreen("#2 ends", func(string) bool {
		return strings.Contains(tr.output(), "\x1b]777;notify;"+from+"batch #2 passed;swim 1 · ")
	})
	tr.waitScreen("no bar once the queue is empty", func(string) bool { return lastProgress(tr.output()) == "\x1b]9;4;0\x07" })
	tr.send("\x03")
	tr.wait()
}

// With notify off (the default), nothing is sent.
func TestWatchNotifyOff(t *testing.T) {
	r := newSerialRepo(t)
	r.script(1, "one", `run "one" true`)
	r.mustSwim("queue", "add", "1")
	tr := r.startTUI(nil, "watch", "--batches", "2")
	tr.waitQueueRow(1, "passed")
	if out := tr.output(); strings.Contains(out, "\x1b]777;") || strings.Contains(out, "\x1b]9;4;") {
		t.Errorf("notify is off, but swim watch sent: %q", out)
	}
	tr.send("\x03")
	tr.wait()
}

// swim run, with notify on: a notification when a lane asks and when the
// run ends, and a progress bar of its lanes, hidden at the end. Off the
// default branch, each title says the branch too.
func TestRunNotifies(t *testing.T) {
	r := newSerialRepo(t)
	from := onBranch(t, r, "fix-dns")
	r.script(1, "one", `if guard ALLOW_ONE "change one"; then run "one changed" true; fi`)
	r.script(2, "two", `run "two" true`)
	r.mustSwim("config", "notify", "true")
	tr := r.startTUI(nil, "all")
	tr.waitScreen("the question's notification", func(string) bool {
		return strings.Contains(tr.output(), "\x1b]777;notify;"+from+"lane 1 needs you;approval of ALLOW_ONE: change one\x07")
	})
	// The notification can come before the view takes keys: an Enter sent
	// then was lost, and the wait for the question ran out (T29, run 116).
	// Wait for the line the view shows when Enter will open the question.
	tr.waitFor("Enter to view it and answer")
	tr.send("\r")
	tr.waitFor("y approve")
	tr.send("y")
	tr.waitFor("run finished · q to close")
	tr.send("q")
	tr.wait()
	out := tr.output()
	contains(t, "swim run's output", out, "\x1b]777;notify;"+from+"run passed;swim 1,2 · 2 passed · ", "\x1b]9;4;")
	if last := lastProgress(out); last != "\x1b]9;4;0\x07" {
		t.Errorf("the bar is left on the tab: %q", last)
	}
}
