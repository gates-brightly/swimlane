package e2e

// T9: secrets don't leak. One run plants secrets of every kind swim masks
// (a variable secret_env_auto names, a multi-line one and one secret_env
// lists), has a lane print them every way it can, drives
// every writer (the plain and YAML streams, status and history, the log
// and archive, the queue and its worker), then reads every file
// swim wrote: none may hold a value, raw or encoded. A file no writer in
// the inventory below accounts for fails too: a new writer must be added
// here, and so checked.

import (
	"encoding/base64"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// leakSecrets are the planted values: each holds characters that URL
// encoding changes, so its encoded forms differ from it.
var leakSecrets = map[string]string{
	"auto": "auto L3ak/v+1x",
	"pem":  "-----BEGIN TEST KEY-----\nPEMline1/L3ak+abcd\nPEMline2/L3ak+efgh\n-----END TEST KEY-----",
	"env":  "envvar L3ak/v+4x",
}

// leakNeedles are the strings no file or output may contain: each value,
// its base64 (standard and URL-safe, unpadded: a prefix of the padded
// form), its URL encodings, and each line of a multi-line one.
func leakNeedles() map[string]string {
	out := map[string]string{}
	for name, v := range leakSecrets {
		forms := []string{v, base64.RawStdEncoding.EncodeToString([]byte(v)), base64.RawURLEncoding.EncodeToString([]byte(v)),
			url.QueryEscape(v), url.PathEscape(v)}
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "L3ak") && l != v {
				forms = append(forms, l)
			}
		}
		for i, f := range forms {
			out[f] = name + []string{" raw", " base64", " base64url", " query-escaped", " path-escaped", " line", " line"}[min(i, 6)]
		}
	}
	return out
}

// leakInventory is every kind of file swim writes in a run like this one,
// by path (slash-separated, relative to the scanned directory, prefixed
// with repo/, config/, home/ or tmp/). A file that matches none is a
// new writer: add it here (after making sure it masks).
var leakInventory = []struct{ what, re string }{
	{"lane scripts and swim.yml (the test's own)", `^repo/(lane\.\d+\.(sh|py)|swim\.yml|lookup\.yml|\.gitignore|\.swim\.lock)$`},
	{"the project log", `^repo/\.swim\.log$`},
	{"lane logs", `^repo/\.swim/logs/agent\d+(\.prev-[^/]+)?(\.check)?\.log$`},
	{"archived logs", `^repo/\.swim/logs/archive/`},
	{"status", `^repo/\.swim/status\.yml$`},
	{"staged lane copies", `^repo/\.swim/runs/r-[^/]+/\d+/`},
	{"kept scripts", `^repo/\.swim/scripts/`},
	{"the chain", `^repo/\.swim/(chain\.jsonl|checkpoints\.jsonl|checkpoints\.sent\.yml)$`},
	{"round outputs", `^repo/\.swim/out/`},
	{"the queue", `^repo/\.swim/queue/`},
	{"lane state (claims hold a pid and its start time)", `^repo/\.swim/(lane\d+\.(pid|claim|stop|msg|end)|lanes/)`},
	{"locks", `^repo/\.swim/(locks/|.*\.lock$)`},
	{"the git shim", `^repo/\.swim/bin/`},
	{"watch state", `^repo/\.swim/watch`},
	{"report earmarks (places in the logs: round lines and hashes)", `^repo/\.swim/earmarks/`},
	{"the config", `^config/swim/config\.yml$`},
	{"the trust pins", `^config/swim/trust`},
}

func TestNoSecretLeaks(t *testing.T) {
	long(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed (the lane URL-encodes with it)")
	}
	r := newRepo(t, "")
	home := t.TempDir()
	// Every temp file swim makes (sockets, staging) under a directory this
	// test reads: short, for unix socket paths.
	tmp, err := os.MkdirTemp("/tmp", "swl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	r.env = append(r.env, "HOME="+home, "TMPDIR="+tmp, "MY_LEAK_VAR="+leakSecrets["env"], "LEAK_API_TOKEN="+leakSecrets["auto"], "MY_LEAK_PEM="+leakSecrets["pem"])
	r.write("swim.yml", "secret_env: [MY_LEAK_VAR, MY_LEAK_PEM]\n", 0o644)

	// The lane prints them every way: raw, to stderr, base64 (standard,
	// unpadded, URL-safe), URL-encoded, split across writes, in step
	// labels, a guard's description, msg, note, an output value, a failing
	// gate and the stop reason.
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: leak everything
# Guards: LEAK_OK describe it
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
V=$LEAK_API_TOKEN
P=$MY_LEAK_PEM
if guard LEAK_OK "describe it ($V)"; then
  :
fi
export V P
msg "msg $V"
swim note --lane 1 "note $V"
output leaked "$V"
run "label $V" bash -c 'echo "raw=$V"; echo "env=$MY_LEAK_VAR"; printf "%s\n" "$P"; echo "err=$V" >&2; sleep 0.3'
run "encodings" bash -c 'for s in "$V" "$MY_LEAK_VAR"; do printf %s "$s" | base64; printf %s "$s" | base64 | tr -d =; printf %s "$s" | base64 | tr "+/" "-_"; python3 -c "import sys,urllib.parse as u; print(u.quote_plus(sys.argv[1])); print(u.quote(sys.argv[1], safe=\"\"))" "$s"; printf "split="; printf %s "${s:0:7}"; printf "%s\n" "${s:7}"; done'
run "sleep" bash -c 'echo "${#V} ${#G}" > `+filepath.Join(tmp, "sleeping")+`; sleep 3'
gate "gate $V" bash -c 'echo "gate saw $V"; false'
summary
`, 0o755)

	needles := leakNeedles()
	check := func(what, s string) {
		t.Helper()
		for n, form := range needles {
			if strings.Contains(s, n) {
				t.Errorf("%s holds the %s secret (%s):\n%s", what, strings.Fields(form)[0], form, s)
				return
			}
		}
	}

	// Linux: while a step sleeps, no process's command line holds a value
	// (the lane passes them in its environment only).
	var procWG sync.WaitGroup
	if runtime.GOOS == "linux" {
		procWG.Add(1)
		go func() {
			defer procWG.Done()
			deadline := time.Now().Add(waitDeadline)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(tmp, "sleeping")); err == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			files, _ := filepath.Glob("/proc/[0-9]*/cmdline")
			for _, f := range files {
				data, _ := os.ReadFile(f)
				check(f, strings.ReplaceAll(string(data), "\x00", " "))
			}
		}()
	}
	// status.yml as it changes: the steps running now.
	stop := make(chan struct{})
	sawStep := make(chan bool, 1)
	go func() {
		saw := false
		defer func() { sawStep <- saw }()
		for {
			data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "status.yml"))
			check("status.yml mid-run", string(data))
			saw = saw || strings.Contains(string(data), "label ***")
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	c := r.cmd(bin, "run", "1", "-y", "--plain")
	c.SysProcAttr = noTTY()
	out, _ := c.CombinedOutput()
	procWG.Wait()
	close(stop)
	if !<-sawStep {
		t.Error("never saw lane 1's step, masked, in status.yml")
	}
	check("swim run --plain", string(out))
	log := r.log(1)
	contains(t, "agent1.log", log, "raw=***", "env=***", "split=***", "gate saw ***")
	if strings.Contains(log, "PASS  sleep") == false {
		t.Fatalf("the lane didn't get as far as the sleep:\n%s", log)
	}

	// Every other way swim shows a run.
	c = r.cmd(bin, "run", "1", "-y", "--yaml")
	c.SysProcAttr = noTTY()
	yout, _ := c.CombinedOutput()
	check("swim run --yaml", string(yout))
	for _, args := range [][]string{{"status"}, {"status", "--yaml"}, {"log", "1"}, {"log", "1", "--yaml"}, {"history"}, {"verify"}, {"plan"}, {"queue"}} {
		o, _ := r.swim(args...)
		check("swim "+strings.Join(args, " "), o)
	}

	// The queue and its worker.
	r.mustSwim("queue", "add", "1", "-y")
	w := r.cmd(bin, "watch", "--once", "--interval", "200ms")
	w.SysProcAttr = noTTY()
	wout, _ := w.CombinedOutput()
	check("swim watch --once", string(wout))
	o, _ := r.swim("archive", "1")
	check("swim archive", o)

	// Every file swim wrote.
	roots := map[string]string{"repo": r.root, "config": envOf(r, "XDG_CONFIG_HOME"), "home": home, "tmp": tmp}
	var unknown []string
	for prefix, dir := range roots {
		filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			rel = prefix + "/" + filepath.ToSlash(rel)
			if fi.IsDir() {
				if rel == "repo/.git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !fi.Mode().IsRegular() {
				return nil // sockets
			}
			data, _ := os.ReadFile(p)
			check(rel, string(data))
			known := false
			for _, k := range leakInventory {
				if regexp.MustCompile(k.re).MatchString(rel) {
					known = true
					break
				}
			}
			if !known && rel != "tmp/sleeping" {
				unknown = append(unknown, rel)
			}
			return nil
		})
	}
	if len(unknown) > 0 {
		t.Errorf("new writer: add it to the leak inventory (leak_test.go) once it masks:\n  %s", strings.Join(unknown, "\n  "))
	}
}
