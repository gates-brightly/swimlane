package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// payload is a lane's attempt to rewrite what the operator reads: erase
// the line and return to its start, backspace, and a right-to-left
// override.
const payload = `"$(printf 'drop prod\033[2K\rlist\b files \342\200\256evil')"`

const payloadSafe = "drop prodlist files evil"

// msgSafe is the payload as a status message: a message is one line, its
// \r a space.
const msgSafe = "drop prod list files evil"

// noControls fails if s carries any of the payload's controls.
func noControls(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"\x1b[2K", "\r", "\b", "\u202e"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s carries %q:\n%q", what, bad, s)
		}
	}
}

// A lane's guard description, step label, command line and status message
// reach the terminal, the log, status.yml and swim history only made safe;
// the chain keeps the text as given.
func TestUntrustedTextIsSafe(t *testing.T) {
	r := newRepo(t, "")
	r.env = append(r.env, "ALLOW_X=1")
	r.script(1, "hostile", `P=`+payload+`
if guard ALLOW_X "$P"; then run "$P" true; fi
run "post" bash -c 'swim msg "$0"; touch posted; while [ ! -e release ]; do sleep 0.05; done' "$P"`)
	l := r.launch(t, nil)
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(r.root, "posted")); return err == nil })
	time.Sleep(100 * time.Millisecond)
	y := r.mustSwim("status", "--yaml")
	contains(t, "status --yaml", y, "message: "+msgSafe)
	noControls(t, "status --yaml", y)
	if strings.Contains(y, `\`) {
		t.Errorf("status --yaml has escaped controls:\n%s", y)
	}
	os.WriteFile(filepath.Join(r.root, "release"), nil, 0o644)
	if code := l.wait(t, 20*time.Second); code != 0 {
		t.Fatalf("exit %d\n%s", code, l.out.String())
	}
	out := l.out.String()
	contains(t, "output", out, "APPROVED  "+payloadSafe+" (ALLOW_X=1)", "PASS  "+payloadSafe)
	noControls(t, "output", out)
	log := r.log(1)
	contains(t, "log", log, "APPROVED  "+payloadSafe+" (ALLOW_X=1)", "PASS  "+payloadSafe, "MSG  "+msgSafe)
	noControls(t, "log", log)
	h := r.mustSwim("history")
	contains(t, "history", h, "Action:    "+payloadSafe)
	noControls(t, "history", h)
	contains(t, "history --yaml (the chain keeps the text as given)", r.mustSwim("history", "--yaml"), `\e[2K\rlist\b`)
}
