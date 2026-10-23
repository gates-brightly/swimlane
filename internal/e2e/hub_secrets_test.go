package e2e

import (
	"bufio"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// startHub runs swim hub (plain output) with state in dir and returns its URL.
func startHub(t *testing.T, r *repo, dir, addr string) (string, func()) {
	t.Helper()
	h := exec.Command(bin, "hub", "--addr", addr, "--token", "t0ken", "--state", dir, "--admin-token", "adm1n", "--no-tui")
	h.Env = r.env
	h.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := h.StdoutPipe()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(out)
	sc.Scan()
	url := regexp.MustCompile(`http://[0-9.:]+`).FindString(sc.Text())
	if url == "" {
		t.Fatalf("no hub URL in %q", sc.Text())
	}
	go func() {
		for sc.Scan() {
		}
	}()
	return url, func() { syscall.Kill(-h.Process.Pid, syscall.SIGTERM); h.Wait() }
}

func TestHubSecrets(t *testing.T) {
	op := newRepo(t, "") // the operator: holds the secret
	secretsKey(t, op)
	op.setSecret("db_password", "hub-granted-pass")
	wk := newRepo(t, "") // the worker: its own config dir, so its own keys
	state := t.TempDir()
	url, stop := startHub(t, op, state, "127.0.0.1:0")
	defer func() { stop() }()
	admin := []string{"--hub", url, "--admin-token", "adm1n"}

	out := wk.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "worker")
	wk.admit(t, url, "worker")
	fp := regexp.MustCompile(`SHA256:\S+`).FindString(out)
	if fp == "" {
		t.Fatalf("enroll printed no key:\n%s", out)
	}

	// Not approved yet: no grant.
	if out, code := op.swim(append([]string{"secret", "grant", "worker", "db_password"}, admin...)...); code == 0 || !strings.Contains(out, "isn't approved") {
		t.Fatalf("grant before approval: exit %d\n%s", code, out)
	}
	if out, code := op.swim(append([]string{"hub", "approve", "worker"}, "--hub", url, "--admin-token", "wrong")...); code == 0 || !strings.Contains(out, "401") {
		t.Fatalf("approve with a bad admin token: exit %d\n%s", code, out)
	}
	contains(t, "approve", op.mustSwim(append([]string{"hub", "approve", "worker"}, admin...)...), fp, "approved worker")
	contains(t, "grant", op.mustSwim(append([]string{"secret", "grant", "worker", "db_password"}, admin...)...), "granted db_password", fp)

	// The hub holds only ciphertext.
	data, _ := os.ReadFile(filepath.Join(state, "state.json"))
	if strings.Contains(string(data), "hub-granted-pass") || !strings.Contains(string(data), "AGE ENCRYPTED") {
		t.Fatalf("hub state:\n%s", data)
	}
	// The shared enroll token can't fetch a worker's grants.
	req, _ := http.NewRequest("GET", url+"/api/grants/"+workerID(t, wk), nil)
	req.Header.Set("Authorization", "Bearer t0ken")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("fetch with the enroll token: %v %v", resp.Status, err)
	}

	// The worker's run gets it from the hub, masked by name.
	wk.script(1, "uses a hub secret", `V=$(swim secret get db_password) || stop "no db password"
run "use" bash -c "echo v=$V"`)
	if out, code := wk.swim("run", "1", "--plain"); code != 0 {
		t.Fatalf("worker run: exit %d\n%s\n%s", code, out, wk.log(1))
	}
	contains(t, "worker log", wk.log(1), "v=***(db_password)")
	hist, _ := os.ReadFile(filepath.Join(wk.root, ".swim.log"))
	contains(t, "worker .swim.log", string(hist), "db_password (hub scope, get)")
	if got := wk.mustSwim("secret", "get", "db_password"); got != "hub-granted-pass" {
		t.Errorf("get outside a run: %q", got)
	}
	audit, _ := os.ReadFile(filepath.Join(state, "audit.log"))
	contains(t, "hub audit", string(audit), "approved", "granted db_password", "fetched db_password")
	contains(t, "grants", op.mustSwim(append([]string{"secret", "grants"}, admin...)...), "worker", "db_password")

	// A restarted hub remembers the approval and the grant.
	stop()
	addr := strings.TrimPrefix(url, "http://")
	url2, stop2 := startHub(t, op, state, addr)
	stop = stop2
	if url2 != url {
		t.Fatalf("hub moved: %s -> %s", url, url2)
	}
	if got := wk.mustSwim("secret", "get", "db_password"); got != "hub-granted-pass" {
		t.Errorf("after a hub restart: %q", got)
	}

	// Revoked: the next get fails.
	op.mustSwim(append([]string{"secret", "revoke", "worker", "db_password"}, admin...)...)
	if out, code := wk.swim("secret", "get", "db_password"); code == 0 {
		t.Errorf("get after revoke: %q", out)
	}
	// A new key withdraws the approval (and its grants).
	op.mustSwim(append([]string{"secret", "grant", "worker", "db_password"}, admin...)...)
	keys, _ := filepath.Glob(filepath.Join(filepath.Dir(secretsFile(wk)), "worker-keys", "*.key"))
	if len(keys) != 1 {
		t.Fatalf("worker keys: %v", keys)
	}
	os.Remove(keys[0])
	// Enrolled already (its own token): it stays admitted, its key approval goes.
	wk.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "worker")
	if out, code := op.swim(append([]string{"secret", "grant", "worker", "db_password"}, admin...)...); code == 0 || !strings.Contains(out, "isn't approved") {
		t.Errorf("after a key change: exit %d\n%s", code, out)
	}
}

// workerID is the id swim hub enroll saved for r.
func workerID(t *testing.T, r *repo) string {
	data, err := os.ReadFile(filepath.Join(r.root, ".swim", "hub.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^id: (\S+)`).FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("no id in hub.yml:\n%s", data)
	}
	return m[1]
}
