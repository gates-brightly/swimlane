package e2e

// T9: secrets don't leak. One run plants secrets of every kind swim knows
// (a vault secret, a multi-line one, a secret_env variable, a CI stand-in
// and a guarded secret), has a lane print them every way it can, drives
// every writer (the plain and YAML streams, status and history, the log
// and archive, the queue and its worker, the hub), then reads every file
// swim wrote: none may hold a value, raw or encoded. A file no writer in
// the inventory below accounts for fails too: a new writer must be added
// here, and so checked.

import (
	"encoding/base64"
	"io"
	"net/http"
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
	"vault":   "vault L3ak/v+1x",
	"pem":     "-----BEGIN TEST KEY-----\nPEMline1/L3ak+abcd\nPEMline2/L3ak+efgh\n-----END TEST KEY-----",
	"env":     "envvar L3ak/v+4x",
	"ci":      "ci L3ak/v+5x",
	"guarded": "guard L3ak/v+3x",
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
// with repo/, config/, home/, tmp/ or hub/). A file that matches none is a
// new writer: add it here (after making sure it masks).
var leakInventory = []struct{ what, re string }{
	{"lane scripts and swim.yml (the test's own)", `^repo/(lane\.\d+\.(sh|py)|swim\.yml|lookup\.yml|\.gitignore|\.swim\.lock)$`},
	{"the project log", `^repo/\.swim\.log$`},
	{"lane logs", `^repo/\.swim/logs/agent\d+(\.prev-[^/]+)?(\.check)?\.log$`},
	{"archived logs", `^repo/\.swim/logs/archive/`},
	{"status", `^repo/\.swim/status\.yml$`},
	{"run records", `^repo/\.swim/runs/[^/]+\.yml$`},
	{"staged lane copies", `^repo/\.swim/runs/r-[^/]+/\d+/`},
	{"kept scripts", `^repo/\.swim/scripts/`},
	{"the chain", `^repo/\.swim/(chain\.jsonl|checkpoints\.jsonl|checkpoints\.sent\.yml)$`},
	{"round outputs", `^repo/\.swim/out/`},
	{"the queue", `^repo/\.swim/queue/`},
	{"lane state (claims hold a pid and its start time)", `^repo/\.swim/(lane\d+\.(pid|claim|stop|msg|end)|lanes/)`},
	{"evidence", `^repo/\.swim/evidence/`},
	{"locks", `^repo/\.swim/(locks/|.*\.lock$)`},
	{"the git shim", `^repo/\.swim/bin/`},
	{"hub enrolment", `^repo/\.swim/(hub\.yml|hub\.key|hub-state\.yml|worker\.key)$`},
	{"watch state", `^repo/\.swim/watch`},
	{"the config", `^config/swim/config\.yml$`},
	{"the secrets store (ciphertext)", `^config/swim/secrets\.yml$`},
	{"the trust pins", `^config/swim/trust`},
	{"hub worker keys (age keys, not secret values)", `^config/swim/worker-keys/[^/]+\.key$`},
	{"the hub's state", `^hub/`},
}

func TestNoSecretLeaks(t *testing.T) {
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
	secretsKey(t, r)
	r.env = append(r.env, "HOME="+home, "TMPDIR="+tmp, "MY_LEAK_VAR="+leakSecrets["env"], "SWIM_SECRET_CI_LEAK="+leakSecrets["ci"])
	r.write("swim.yml", "secret_env: [MY_LEAK_VAR]\nsecrets:\n  LEAK_GUARD: { guard: LEAK_OK }\n", 0o644)
	r.setSecret("leak_vault", leakSecrets["vault"])
	r.setSecret("leak_pem", leakSecrets["pem"])
	r.setSecret("LEAK_GUARD", leakSecrets["guarded"])

	// The lane prints them every way: raw, to stderr, base64 (standard,
	// unpadded, URL-safe), URL-encoded, split across writes, in step
	// labels, a guard's description, msg, note, an output value, a failing
	// gate, a handler and the stop reason.
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 3
# Round: leak everything
# Guards: LEAK_OK release the guarded one
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
on_failure() { run "handler $V" bash -c 'echo "handler=$V failed=$SWIM_FAILED_STEP stop=$SWIM_STOP_REASON"'; }
V=$(swim secret get leak_vault) || stop "no vault"
P=$(swim secret get leak_pem) || stop "no pem"
C=$(swim secret get ci_leak) || stop "no ci"
G=
if guard LEAK_OK "release it ($V)"; then
  G=$(swim secret get LEAK_GUARD) || stop "no guarded"
fi
export V P C G
msg "msg $V"
swim note --lane 1 "note $V"
output leaked "$V"
run "label $V" bash -c 'echo "raw=$V"; echo "env=$MY_LEAK_VAR"; echo "ci=$C"; echo "guarded=$G"; printf "%s\n" "$P"; echo "err=$V" >&2'
run "encodings" bash -c 'for s in "$V" "$G" "$C" "$MY_LEAK_VAR"; do printf %s "$s" | base64; printf %s "$s" | base64 | tr -d =; printf %s "$s" | base64 | tr "+/" "-_"; python3 -c "import sys,urllib.parse as u; print(u.quote_plus(sys.argv[1])); print(u.quote(sys.argv[1], safe=\"\"))" "$s"; printf "split="; printf %s "${s:0:7}"; printf "%s\n" "${s:7}"; done'
run "sleep" bash -c 'echo "${#V} ${#G}" > `+filepath.Join(tmp, "sleeping")+`; sleep 3'
gate "gate $V" bash -c 'echo "gate saw $G"; false'
summary
`, 0o755)

	// An SDK lane: an in-process step's label (status.yml's current step,
	// the hub) and output, and a guarded secret refused.
	r.env = append(r.env, "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
	r.write("lane.2.py", `#!/usr/bin/env python3
# Round: leak from python
import time
import swim
with swim.round() as r:
    v = swim.secret.get("leak_vault")
    with r.step("in-process " + v) as s:
        s.log("logged " + v)
        time.sleep(1.5)
    try:
        swim.secret.get("LEAK_GUARD")
    except swim.SwimError as e:
        r.run("refusal", ["echo", str(e)])
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
			deadline := time.Now().Add(60 * time.Second)
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
			saw = saw || strings.Contains(string(data), "in-process ***(leak_vault)")
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	c := r.cmd(bin, "run", "1", "2", "-y", "--plain")
	c.SysProcAttr = noTTY()
	out, _ := c.CombinedOutput()
	procWG.Wait()
	close(stop)
	if !<-sawStep {
		t.Error("never saw lane 2's in-process step in status.yml")
	}
	contains(t, "agent2.log", r.log(2), "logged ***(leak_vault)", "refused: LEAK_GUARD is guarded by LEAK_OK")
	check("swim run --plain", string(out))
	log := r.log(1)
	contains(t, "agent1.log", log, "raw=***(leak_vault)", "guarded=***(LEAK_GUARD)", "env=***", "ci=***(ci_leak)",
		"split=***(leak_vault)", "handler=***(leak_vault)", "gate saw ***(LEAK_GUARD)")
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

	// The queue and its worker, enrolled with a hub: the hub's snapshots
	// (tails, events, messages) and its pages.
	hubState := t.TempDir()
	hubURL, stopHub := startHub(t, r, hubState, "127.0.0.1:0")
	defer stopHub()
	r.mustSwim("hub", "enroll", hubURL, "--token", "t0ken", "--name", "leaky")
	r.mustSwim("queue", "add", "1", "-y")
	w := r.cmd(bin, "watch", "--once", "--interval", "200ms")
	w.SysProcAttr = noTTY()
	wout, _ := w.CombinedOutput()
	check("swim watch --once", string(wout))
	for _, p := range []string{"/api/workers", "/"} {
		resp, err := http.Get(hubURL + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		check("hub "+p, string(body))
	}
	o, _ := r.swim("archive", "1")
	check("swim archive", o)
	stopHub()

	// Every file swim wrote.
	roots := map[string]string{"repo": r.root, "config": envOf(r, "XDG_CONFIG_HOME"), "home": home, "tmp": tmp, "hub": hubState}
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
