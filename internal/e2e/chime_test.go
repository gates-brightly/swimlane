package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// swim config <key> [value]: get, set, reject (features/chime.md).
func TestConfigKeys(t *testing.T) {
	r := newRepo(t, "")
	if got := strings.TrimSpace(r.mustSwim("config", "chime")); got != "false" {
		t.Errorf("default chime = %q", got)
	}
	contains(t, "set chime", r.mustSwim("config", "chime", "failure"), "chime: failure  (defaults in ")
	if got := strings.TrimSpace(r.mustSwim("config", "chime")); got != "failure" {
		t.Errorf("chime after set = %q", got)
	}
	contains(t, "config", r.mustSwim("config"), "chime: failure", "chime_style: bell", "chime_min_s: 10")

	for _, bad := range [][]string{
		{"chime", "maybe"}, {"chime_style", "kazoo"}, {"chime_min_s", "-1"}, {"colour", "red"}, {"colour"},
	} {
		out, code := r.swim(append([]string{"config"}, bad...)...)
		if code == 0 {
			t.Errorf("config %v accepted:\n%s", bad, out)
		}
		switch bad[0] {
		case "chime":
			contains(t, "bad chime", out, "true, false or failure")
		case "chime_style":
			contains(t, "bad style", out, "bell, sound, notify")
		case "colour":
			contains(t, "unknown key", out, "keys: lanes, chime, chime_style, chime_min_s")
		}
	}
	if got := strings.TrimSpace(r.mustSwim("config", "chime")); got != "failure" {
		t.Errorf("a rejected value changed chime: %q", got)
	}

	// --repo writes this repo's section, which wins over defaults.
	contains(t, "--repo", r.mustSwim("config", "chime_min_s", "0", "--repo"), "chime_min_s: 0  (repo "+r.root)
	contains(t, "shadowed", r.mustSwim("config", "chime_min_s", "30"), "this repo's section sets chime_min_s: 0")
	if got := strings.TrimSpace(r.mustSwim("config", "chime_min_s")); got != "0" {
		t.Errorf("chime_min_s = %q", got)
	}

	// lanes works as a key, with --lanes kept as an alias.
	contains(t, "config lanes", r.mustSwim("config", "lanes", "6"), "lanes: 4 -> 6")
	if got := strings.TrimSpace(r.mustSwim("config", "lanes")); got != "6" {
		t.Errorf("lanes = %q", got)
	}
	contains(t, "config --lanes", r.mustSwim("config", "--lanes", "5"), "lanes: 6 -> 5")
	if out, code := r.swim("config", "lanes", "100"); code == 0 || !strings.Contains(out, "1..99") {
		t.Errorf("lanes 100: %d %s", code, out)
	}

	cfg, _ := os.ReadFile(filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml"))
	contains(t, "config file", string(cfg), "# swim configuration", "# Env vars recorded", "chime: failure", "chime_min_s: 30", "chime_min_s: 0", "lanes: 5")
}

// No terminal, no bell: even with chime on and no minimum.
func TestChimeNeverIntoAPipe(t *testing.T) {
	r := newRepo(t, "")
	r.env = append(r.env, "CI=")
	r.mustSwim("config", "chime", "true")
	r.mustSwim("config", "chime_min_s", "0")
	r.script(1, "passes", `run "ok" true`)
	r.script(2, "fails", `run "nope" false`)
	out := r.mustSwim("run", "1", "--chime")
	contains(t, "run", out, "swim summary")
	if strings.Contains(out, "\a") {
		t.Errorf("bell written to a pipe:\n%q", out)
	}
	out, code := r.swim("all")
	if code != 1 || strings.Contains(out, "\a") {
		t.Errorf("failing run: exit %d, bell in pipe: %q", code, out)
	}
	if out, code := r.swim("run", "1", "--chime", "--no-chime"); code != 2 || !strings.Contains(out, "--chime or --no-chime") {
		t.Errorf("--chime --no-chime: %d %s", code, out)
	}
}
