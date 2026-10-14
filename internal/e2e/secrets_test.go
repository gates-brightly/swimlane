package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
