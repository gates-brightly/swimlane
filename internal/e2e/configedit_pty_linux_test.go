package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim config edit shows every setting at its default, changes one in
// place, and writes only what differs from the default: a value stepped
// or typed back to the default, or reset with d, leaves the file. The
// first write backs the file up as config.yml-1.bak, once a session.
func TestConfigEdit(t *testing.T) {
	r := newSerialRepo(t, "")
	cfg := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	file := func() string { b, _ := os.ReadFile(cfg); return string(b) }
	before := "# mine\ndefaults: {}\n"
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte(before), 0o644)
	tr := r.startTUI(nil, "config", "edit")
	tr.waitFor("interrupt          graceful  (default)")
	tr.waitFor("notify             false  (default)")

	tr.send("\r") // interrupt: graceful -> immediate
	tr.waitFor("interrupt: immediate (written)")
	contains(t, "config.yml", file(), "interrupt: immediate")
	if b, err := os.ReadFile(cfg + "-1.bak"); err != nil || string(b) != before {
		t.Errorf("the first write's backup: %q, %v", b, err)
	}
	tr.send("\r") // and back: the default, so not written
	tr.waitFor("interrupt: graceful, the default (not written)")
	if strings.Contains(file(), "interrupt:") {
		t.Errorf("the default was written:\n%s", file())
	}

	tr.send("j\r") // interrupt_grace: typed
	tr.waitScreen("the prompt", func(string) bool { return strings.Contains(tr.output(), "interrupt_grace (now 5s; empty keeps it): ") })
	tr.send("7s\r")
	tr.waitFor("interrupt_grace: 7s (written)")
	contains(t, "config.yml", file(), "interrupt_grace: 7s")
	tr.send("d")
	tr.waitFor("interrupt_grace: back to the default, 5s")
	if strings.Contains(file(), "interrupt_grace") {
		t.Errorf("d left the key:\n%s", file())
	}

	tr.send("j\r") // term_grace: a bad value says why and writes nothing
	tr.waitScreen("the prompt", func(string) bool { return strings.Contains(tr.output(), "term_grace (now 10s; empty keeps it): ") })
	tr.send("soon\r")
	tr.waitFor(`term_grace must be a duration`)
	if strings.Contains(file(), "term_grace") {
		t.Errorf("a bad value was written:\n%s", file())
	}
	tr.send("q")
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if baks, _ := filepath.Glob(cfg + "-*.bak"); len(baks) != 1 {
		t.Errorf("one session, one backup: %v", baks)
	}
}
