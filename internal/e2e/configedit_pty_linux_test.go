package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim config on a terminal is swim watch's picker over every setting: its
// value, default and what it takes. It changes one in place (choices with
// Enter or ← →, a value typed on the bottom line under the rows), and
// writes only what differs from the default: a value stepped or typed
// back to the default, or reset with d, leaves the file. The first write
// backs the file up as config.yml-1.bak, once a session, and the screen
// stays up from first key to q.
func TestConfigEdit(t *testing.T) {
	r := newSerialRepo(t, "")
	cfg := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	file := func() string { b, _ := os.ReadFile(cfg); return string(b) }
	before := "# mine\ndefaults: {}\n"
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte(before), 0o644)
	tr := r.startTUI(nil, "config")
	// row waits for a setting's row, its columns as words.
	row := func(cols ...string) {
		t.Helper()
		want := strings.Join(cols, " ")
		tr.waitScreen(want, func(s string) bool {
			for _, l := range strings.Split(s, "\n") {
				if strings.Contains(strings.Join(strings.Fields(l), " "), want) {
					return true
				}
			}
			return false
		})
	}
	row("interrupt", "graceful", "graceful", "graceful | immediate")
	row("signing_key", "—", "—", "SSH key path")
	row("claim_lease", "2h", "2h", "duration, 1m or more")
	row("outputs_keep", "14d", "14d")

	tr.send("\r") // interrupt: graceful -> immediate
	tr.waitFor("interrupt: immediate (written)")
	row("interrupt", "immediate", "graceful")
	contains(t, "config.yml", file(), "interrupt: immediate")
	if b, err := os.ReadFile(cfg + "-1.bak"); err != nil || string(b) != before {
		t.Errorf("the first write's backup: %q, %v", b, err)
	}
	tr.send("\x1b[D") // ← and back: the default, so not written
	tr.waitFor("interrupt: graceful, the default (not written)")
	if strings.Contains(file(), "interrupt:") {
		t.Errorf("the default was written:\n%s", file())
	}

	tr.send("j\r") // interrupt_grace: typed on the bottom line, the rows still up
	tr.waitScreen("the field", func(s string) bool {
		return strings.Contains(s, "interrupt_grace: 5s") && strings.Contains(s, "Enter saves · Esc cancels") && strings.Contains(s, "term_grace")
	})
	tr.send("\x7f\x7f7s\r")
	tr.waitFor("interrupt_grace: 7s (written)")
	contains(t, "config.yml", file(), "interrupt_grace: 7s")
	tr.send("d")
	tr.waitFor("interrupt_grace: back to the default, 5s")
	if strings.Contains(file(), "interrupt_grace") {
		t.Errorf("d left the key:\n%s", file())
	}

	tr.send("j\r") // term_grace: a bad value says why and writes nothing
	tr.waitFor("term_grace: 10s")
	tr.send("\x7f\x7f\x7fsoon\r")
	tr.waitFor(`term_grace must be a duration`)
	if strings.Contains(file(), "term_grace") {
		t.Errorf("a bad value was written:\n%s", file())
	}
	tr.send("\r") // Esc closes the field, not swim config
	tr.waitFor("Enter saves")
	tr.send("\x1b")
	tr.waitFor("d default · t test notify · q quit")
	tr.send("q")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if n := strings.Count(tr.output(), "\x1b[?1049h"); n != 1 {
		t.Errorf("the screen came up %d times, not once", n)
	}
	if baks, _ := filepath.Glob(cfg + "-*.bak"); len(baks) != 1 {
		t.Errorf("one session, one backup: %v", baks)
	}
}
