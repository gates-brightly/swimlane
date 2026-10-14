package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// noTTY runs a command in a new session, with no controlling terminal:
// nothing can prompt on /dev/tty.
func noTTY() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// secretsKey gives the repo a throwaway secrets key (never the
// developer's own) and returns its path.
func secretsKey(t *testing.T, r *repo) string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "id_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	r.mustSwim("config", "secrets_key", p)
	return p
}

// setSecret stores value as name, from stdin.
func (r *repo) setSecret(name, value string, flags ...string) string {
	r.t.Helper()
	c := r.cmd(bin, append([]string{"secret", "set", name}, flags...)...)
	c.Stdin = strings.NewReader(value + "\n")
	out, err := c.CombinedOutput()
	if err != nil {
		r.t.Fatalf("swim secret set %s: %v\n%s", name, err, out)
	}
	return string(out)
}

func secretsFile(r *repo) string {
	for _, kv := range r.env {
		if x, ok := strings.CutPrefix(kv, "XDG_CONFIG_HOME="); ok {
			return filepath.Join(x, "swim", "secrets.yml")
		}
	}
	return ""
}

func TestSecretCLI(t *testing.T) {
	r := newRepo(t, "")
	secretsKey(t, r)
	r.setSecret("db_password", "s3cret-db-pass")
	r.setSecret("api_token", "all-repos-token", "--all")
	r.setSecret("api_token", "this-repo-token")

	data, _ := os.ReadFile(secretsFile(r))
	for _, v := range []string{"s3cret-db-pass", "all-repos-token", "this-repo-token"} {
		if strings.Contains(string(data), v) {
			t.Fatalf("secrets.yml holds %q in the clear", v)
		}
	}
	if fi, err := os.Stat(secretsFile(r)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("secrets.yml: %v %v", fi.Mode().Perm(), err)
	}

	if got := r.mustSwim("secret", "get", "db_password"); got != "s3cret-db-pass" {
		t.Errorf("get db_password: %q", got)
	}
	if got := r.mustSwim("secret", "get", "api_token"); got != "this-repo-token" {
		t.Errorf("the repo's own api_token should win: %q", got)
	}
	ls := r.mustSwim("secret", "ls")
	contains(t, "ls", ls, "db_password", "api_token", "hidden by this repo's own")
	for _, v := range []string{"s3cret", "token\n"} {
		if strings.Contains(ls, v) {
			t.Errorf("ls shows a value (%q):\n%s", v, ls)
		}
	}
	if out, code := r.swim("secret", "get", "nope"); code == 0 || !strings.Contains(out, "no secret nope") {
		t.Errorf("get nope: exit %d %s", code, out)
	}
	// SWIM_SECRET_<NAME> stands in (CI).
	c := r.cmd(bin, "secret", "get", "db_password")
	c.Env = append(c.Env, "SWIM_SECRET_DB_PASSWORD=from-ci")
	if out, _ := c.Output(); string(out) != "from-ci" {
		t.Errorf("env stand-in: %q", out)
	}

	r.mustSwim("secret", "rm", "api_token")
	if got := r.mustSwim("secret", "get", "api_token"); got != "all-repos-token" {
		t.Errorf("after rm, the --all one shows: %q", got)
	}
	if _, code := r.swim("secret", "rm", "api_token"); code == 0 {
		t.Error("removed a repo secret twice")
	}
	data, _ = os.ReadFile(filepath.Join(r.root, ".swim.log"))
	log := string(data)
	contains(t, ".swim.log", log, "secret-set   db_password", "secret-rm    api_token")
	if strings.Contains(log, "s3cret") {
		t.Error(".swim.log holds a value")
	}
}

const secretsLane = `#!/usr/bin/env bash
# swim: syntax 2
# Round:   Use secrets three ways
# Secrets: DB_PASSWORD=db_password
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
stage check
run "header env" bash -c 'echo "env has: $DB_PASSWORD"; printf %s "$DB_PASSWORD" | base64'
run "placeholder" echo "token is _swim.api_token"
TOK=$(swim secret get api_token)
run "via get" bash -c "echo got $TOK"
summary
`

func TestSecretsInRun(t *testing.T) {
	r := newRepo(t, "")
	secretsKey(t, r)
	r.setSecret("db_password", "s3cret-db-pass")
	r.setSecret("api_token", "all-repos-token", "--all")
	r.write("lane.1.sh", secretsLane, 0o755)
	out, code := r.swim("run", "1", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out, r.log(1))
	}
	log := r.log(1)
	contains(t, "log", log, "env has: ***(db_password)", "token is ***(api_token)", "got ***(api_token)",
		"$ echo 'token is _swim.api_token'", "secrets: DB_PASSWORD=db_password")
	if !strings.Contains(log, "| ***(db_password)\n") {
		t.Errorf("the base64 form isn't masked:\n%s", log)
	}
	st, _ := os.ReadFile(filepath.Join(r.root, ".swim", "status.yml"))
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	for _, where := range []string{out, log, string(st), string(hist)} {
		for _, v := range []string{"s3cret-db-pass", "all-repos-token", "czNjcmV0"} {
			if strings.Contains(where, v) {
				t.Fatalf("%q leaked:\n%s", v, where)
			}
		}
	}
	contains(t, ".swim.log", string(hist), "db_password (repo scope, Secrets: DB_PASSWORD)",
		"api_token (all scope, step)", "api_token (all scope, get)")
}

func TestSecretsMissingStopsRun(t *testing.T) {
	r := newRepo(t, "")
	secretsKey(t, r)
	r.setSecret("db_password", "s3cret-db-pass")
	r.script(1, "needs a missing secret", `run "x" true`)
	data, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(strings.Replace(string(data), "# Round:", "# Secrets: nope\n# Round:", 1)), 0o755)
	out, code := r.swim("run", "1", "--plain")
	if code == 0 || !strings.Contains(out, "swim 1: Secrets: nope: no secret nope") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if r.log(1) != "" {
		t.Error("the lane started")
	}
}

// In CI there's no key: SWIM_SECRET_<NAME> supplies the secret, and it's
// masked by name like a stored one.
func TestSecretsFromCIEnv(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "ci", `run "use" bash -c 'echo "token: $CI_TOKEN"'`)
	data, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(strings.Replace(string(data), "# Round:", "# Secrets: ci_token\n# Round:", 1)), 0o755)
	r.env = append(r.env, "SWIM_SECRET_CI_TOKEN=ci-supplied-value")
	out, code := r.swim("run", "1", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, "log", r.log(1), "token: ***(ci_token)")
	if strings.Contains(out+r.log(1), "ci-supplied-value") {
		t.Error("the CI secret leaked")
	}
}

// A passphrase-protected key with nobody at a terminal can't decrypt: a
// lane that declares secrets stops the run and says why.
func TestSecretsPassphraseKeyUnattended(t *testing.T) {
	r := newRepo(t, "")
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "id_ed25519")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "pass phrase", "-f", p).Run()
	r.mustSwim("config", "secrets_key", p)
	r.setSecret("db_password", "s3cret-db-pass") // encrypting needs only the .pub
	r.script(1, "needs it", `run "x" true`)
	data, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))
	os.WriteFile(filepath.Join(r.root, "lane.1.sh"), []byte(strings.Replace(string(data), "# Round:", "# Secrets: db_password\n# Round:", 1)), 0o755)
	c := r.cmd(bin, "run", "1", "--plain")
	c.SysProcAttr = noTTY()
	outB, err := c.CombinedOutput()
	out := string(outB)
	if err == nil || !strings.Contains(out, "needs a passphrase") {
		t.Fatalf("err %v:\n%s", err, out)
	}
	// A lane that uses no secrets still runs (nothing is asked).
	r.script(2, "plain", `run "x" true`)
	if out, code := r.swim("run", "2", "--plain"); code != 0 {
		t.Errorf("a lane without secrets: exit %d\n%s", code, out)
	}
}

// A lane in another language gets them the same way (wrapped by swim).
func TestSecretsPythonLane(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	r := newRepo(t, "")
	secretsKey(t, r)
	r.setSecret("api_token", "py-token-value")
	r.write("lane.1.py", `#!/usr/bin/env python3
# Round:   python secrets
# Secrets: api_token
import os, subprocess
print("env:", os.environ["API_TOKEN"])
got = subprocess.run(["swim", "secret", "get", "api_token"], capture_output=True, text=True).stdout
subprocess.run(["swim", "run", "placeholder", "--", "echo", "arg _swim.api_token"], check=True)
print("got:", got)
`, 0o755)
	out, code := r.swim("run", "1", "--plain")
	if code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out, r.log(1))
	}
	log := r.log(1)
	contains(t, "log", log, "arg ***(api_token)")
	// The program's own lines go to the terminal, masked there too.
	contains(t, "output", out, "env: ***(api_token)", "got: ***(api_token)")
	if strings.Contains(out+log, "py-token-value") {
		t.Errorf("leaked:\n%s", log)
	}
}
