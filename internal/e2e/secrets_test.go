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
# Round: Use secrets from bash
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
stage check
DB=$(swim secret get db_password)
TOK=$(swim secret get api_token)
run "use them" bash -c "echo db=$DB; printf %s $DB | base64; echo tok=$TOK"
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
	contains(t, "log", log, "db=***(db_password)", "tok=***(api_token)", "| ***(db_password)\n")
	st, _ := os.ReadFile(filepath.Join(r.root, ".swim", "status.yml"))
	hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
	for _, where := range []string{out, log, string(st), string(hist)} {
		for _, v := range []string{"s3cret-db-pass", "all-repos-token", "czNjcmV0"} {
			if strings.Contains(where, v) {
				t.Fatalf("%q leaked:\n%s", v, where)
			}
		}
	}
	contains(t, ".swim.log", string(hist), "db_password (repo scope, get)", "api_token (all scope, get)")

	// A secret that isn't stored fails the get, and the step that needed it.
	// (bash has no set -e in lanes: check the get, as the docs show).
	lane2 := strings.Replace(secretsLane, "lane_init 1", "lane_init 2", 1)
	lane2 = strings.Replace(lane2, "DB=$(swim secret get db_password)", `DB=$(swim secret get nope) || stop "no secret nope"`, 1)
	r.write("lane.2.sh", lane2, 0o755)
	out, code = r.swim("run", "2", "--plain")
	if code == 0 || !strings.Contains(out, "no secret nope") || strings.Contains(r.log(2), "use them") {
		t.Errorf("missing secret: exit %d\n%s", code, out)
	}
}

// In CI there's no key: SWIM_SECRET_<NAME> supplies the secret, and it's
// masked by name like a stored one.
func TestSecretsFromCIEnv(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "ci", `T=$(swim secret get ci_token)
run "use" bash -c "echo token: $T"`)
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

// A passphrase-protected key with nobody at a terminal can't decrypt: the
// run says why, and a get fails; a lane that uses no secrets is untouched.
func TestSecretsPassphraseKeyUnattended(t *testing.T) {
	r := newRepo(t, "")
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "id_ed25519")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "pass phrase", "-f", p).Run()
	r.mustSwim("config", "secrets_key", p)
	r.setSecret("db_password", "s3cret-db-pass") // encrypting needs only the .pub
	r.script(1, "needs it", `T=$(swim secret get db_password) || stop "no secret"
run "x" true`)
	c := r.cmd(bin, "run", "1", "--plain")
	c.SysProcAttr = noTTY()
	outB, err := c.CombinedOutput()
	if out := string(outB); err == nil || !strings.Contains(out, "needs a passphrase") {
		t.Fatalf("err %v:\n%s", err, out)
	}
	r.script(2, "plain", `run "x" true`)
	c = r.cmd(bin, "run", "2", "--plain")
	c.SysProcAttr = noTTY()
	if out, err := c.CombinedOutput(); err != nil || strings.Contains(string(out), "passphrase") {
		t.Errorf("a lane without secrets: %v\n%s", err, out)
	}
}
