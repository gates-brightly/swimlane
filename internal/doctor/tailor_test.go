package doctor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/finding"
)

// writeLane writes lane n's script: the bash library loaded, then body.
func writeLane(t *testing.T, root string, n int, body string) {
	t.Helper()
	src := fmt.Sprintf(`#!/usr/bin/env bash
# swim: syntax 4
# Round: probe %[1]d
# Timeout: 30m
_swim_lib=$("${SWIM_BIN:-swim}" _lib) || { echo "swim not found" >&2; exit 1; }
eval "$_swim_lib"
lane_init %[1]d
stage snapshot
%[2]s
summary
`, n, body)
	if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("lane.%d.sh", n)), []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
}

// bash 3.2 matters only where a lane uses a bash 4 feature: doctor names
// the lane and line. No bash at all is an error.
func TestBash(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	writeLane(t, root, 2, "declare -A seen\nsnapshot \"ls\" ls")
	writeLane(t, root, 3, "snapshot \"ls\" ls")
	o := opts(root)
	if b := only(Run(o), "bash"); len(b) != 0 {
		t.Errorf("bash 5: %+v", b)
	}
	o.BashVersion = "3.2"
	b := only(Run(o), "bash")
	if len(b) != 1 || b[0].File != "lane.2.sh" || b[0].Line != 9 || b[0].Level != finding.Warn ||
		!strings.Contains(b[0].Message, "`declare -A` (associative arrays) is bash 4, and the bash on PATH here is 3.2") {
		t.Errorf("bash 3.2: %+v", b)
	}
	os.Remove(filepath.Join(root, "lane.2.sh"))
	if b := only(Run(o), "bash"); len(b) != 0 {
		t.Errorf("bash 3.2, no bash 4 in lanes: %+v", b)
	}
	o.BashVersion = ""
	o.defaults()
	o.BashVersion = ""
	if b := checkBash(&o); len(b) != 1 || b[0].Level != finding.Error {
		t.Errorf("no bash: %+v", b)
	}
}

// GNU-only forms are named on a Mac (or BSD) whose tool isn't GNU's; on
// Linux, or with GNU's tool on PATH, they're fine.
func TestGNUOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	writeLane(t, root, 1, "run \"edit\" sed -i 's/a/b/' notes.txt\nrun \"slow\" timeout 30 make check")
	o := opts(root)
	o.Host = "darwin/arm64"
	o.GNU = func(tool string) bool { return tool == "timeout" } // coreutils' timeout, BSD sed
	g := only(Run(o), "gnu-only")
	if len(g) != 1 || g[0].File != "lane.1.sh" || g[0].Line != 9 || !strings.Contains(g[0].Message, "`sed -i` with no suffix is GNU-only") || !strings.Contains(g[0].Fix, "sed -i.bak") {
		t.Errorf("mac: %+v", g)
	}
	o.GNU = func(string) bool { return false }
	if g := only(Run(o), "gnu-only"); len(g) != 2 {
		t.Errorf("stock mac: %+v", g)
	}
	o.Host = "linux/amd64"
	if g := only(Run(o), "gnu-only"); len(g) != 0 {
		t.Errorf("linux: %+v", g)
	}
}

// config.yml's unknown keys are named with their line.
func TestConfigKeys(t *testing.T) {
	root := t.TempDir()
	o := opts(root)
	if c := only(Run(o), "config-key"); len(c) != 0 {
		t.Errorf("no file: %+v", c)
	}
	os.WriteFile(o.Cfg.Path, []byte("defaults:\n  notify_url: https://ntfy.sh/x\n  notfy: true\nrepos: {}\n"), 0o644)
	c := only(Run(o), "config-key")
	if len(c) != 1 || c[0].File != o.Cfg.Path || c[0].Line != 3 || c[0].Message != "unknown key notfy under defaults: swim ignores it" {
		t.Errorf("a typo: %+v", c)
	}
}

// notify_url answers or doesn't; notify_command's program is there or
// isn't.
func TestNotifyTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) }))
	defer srv.Close()
	o := opts(t.TempDir())
	o.NotifyTimeout = 2 * time.Second
	if n := only(Run(o), "notify"); len(n) != 0 {
		t.Errorf("nothing set: %+v", n)
	}
	o.Cfg.NotifyURL = srv.URL
	for _, n := range only(Run(o), "notify") {
		if strings.Contains(n.Message, "doesn't answer") {
			t.Errorf("a server that answers: %+v", n)
		}
	}
	srv.Close()
	if n := only(Run(o), "notify"); len(n) == 0 || !strings.Contains(n[len(n)-1].Message, "doesn't answer") {
		t.Errorf("a server that's gone: %+v", n)
	}
	o.Cfg.NotifyURL = ""
	o.Cfg.NotifyCommand = "TOPIC=x sh -c true"
	if n := only(Run(o), "notify"); len(n) != 0 {
		t.Errorf("sh: %+v", n)
	}
	o.Cfg.NotifyCommand = "~/no/such/notifier --flag"
	if n := only(Run(o), "notify"); len(n) != 1 || !strings.Contains(n[0].Message, "runs ~/no/such/notifier, which isn't on PATH") {
		t.Errorf("a missing notifier: %+v", n)
	}
}

// Leftover state is named, and --fix deletes just it: never a lane log,
// the chain, status.yml or the queue.
func TestLeftoverAndFix(t *testing.T) {
	root := t.TempDir()
	swim := filepath.Join(root, ".swim")
	for _, d := range []string{"groups", "observations", "earmarks", "out/job", "logs", "queue", "claims", "workers", "scripts", "snapshots", "signoffs"} {
		os.MkdirAll(filepath.Join(swim, d), 0o755)
	}
	for _, f := range []string{"hub.yml", "headless.log", "lane1.2.claim", "lane1.claim", "chain.jsonl", "status.yml", "logs/agent1.log", "watch.lock"} {
		os.WriteFile(filepath.Join(swim, f), []byte("x\n"), 0o644)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	os.WriteFile(filepath.Join(swim, "claims", "lane.4.yml"), []byte("owner: zach\nsession: zach@mac\nexpires: \""+past+"\"\n"), 0o644)
	os.WriteFile(filepath.Join(swim, "claims", "lane.5.yml"), []byte("owner: zach\nsession: zach@mac\nexpires: \""+future+"\"\n"), 0o644)
	os.WriteFile(filepath.Join(swim, "workers", "far-1.yml"), []byte("host: far-away\npid: 1\nheartbeat: \""+past+"\"\nlease: 2m\n"), 0o644)
	os.WriteFile(filepath.Join(swim, "workers", "far-2.yml"), []byte("host: far-away\npid: 2\nheartbeat: \""+time.Now().UTC().Format(time.RFC3339)+"\"\nlease: 2m\n"), 0o644)

	o := opts(root)
	fs := Run(o)
	var got []string
	for _, f := range fs {
		if f.Code == "leftover" || f.Code == "claim-expired" || f.Code == "watch-gone" {
			got = append(got, f.Code+" "+f.File)
		}
	}
	want := "claim-expired .swim/claims/lane.4.yml watch-gone .swim/workers/far-1.yml " +
		"leftover .swim/earmarks/ leftover .swim/groups/ leftover .swim/headless.log leftover .swim/hub.yml leftover .swim/lane1.2.claim leftover .swim/observations/ leftover .swim/out/"
	if strings.Join(got, " ") != want {
		t.Fatalf("found:\n%s\nwant:\n%s", strings.Join(got, " "), want)
	}
	done, err := Fix(o, fs)
	if err != nil || len(done) != 9 {
		t.Fatalf("fix: %v %v", done, err)
	}
	for _, gone := range []string{"groups", "hub.yml", "out", "lane1.2.claim", "claims/lane.4.yml", "workers/far-1.yml"} {
		if _, err := os.Stat(filepath.Join(swim, gone)); !os.IsNotExist(err) {
			t.Errorf("%s kept", gone)
		}
	}
	for _, kept := range []string{"lane1.claim", "chain.jsonl", "status.yml", "logs/agent1.log", "queue", "scripts", "snapshots", "signoffs", "watch.lock", "claims/lane.5.yml", "workers/far-2.yml"} {
		if _, err := os.Stat(filepath.Join(swim, kept)); err != nil {
			t.Errorf("%s removed", kept)
		}
	}
	// A finding naming anything else is refused.
	if ok, _ := o.removeLeftover(finding.Finding{Code: "leftover", File: ".swim/status.yml"}); ok {
		t.Error("removed status.yml")
	}
	if ok, _ := o.removeLeftover(finding.Finding{Code: "leftover", File: ".swim/logs/hub.yml"}); ok {
		t.Error("removed a file below .swim/")
	}
}
