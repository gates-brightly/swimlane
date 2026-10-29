package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// confirmAgent starts an ssh-agent holding key (and no file of it) added
// with ssh-add -c: each use asks the operator, here an askpass that says
// yes once for each allow() and no otherwise, and counts the questions.
type confirmAgent struct {
	sock, dir string
}

func newConfirmAgent(t *testing.T, key string) *confirmAgent {
	t.Helper()
	for _, b := range []string{"ssh-agent", "ssh-add"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " not installed")
		}
	}
	dir, err := os.MkdirTemp("", "swim-ag") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	a := &confirmAgent{sock: filepath.Join(dir, "s"), dir: dir}
	askpass := filepath.Join(dir, "askpass")
	os.WriteFile(askpass, []byte("#!/bin/sh\necho asked >> \""+dir+"/asked\"\nrm \""+dir+"/allow\" 2>/dev/null\n"), 0o755)
	agent := exec.Command("ssh-agent", "-D", "-a", a.sock)
	agent.Env = append(os.Environ(), "SSH_ASKPASS="+askpass, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
	if !waitIn(dir, func() bool { _, err := os.Stat(a.sock); return err == nil }) {
		t.Fatal("ssh-agent never made its socket")
	}
	add := exec.Command("ssh-add", "-c", key)
	add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+a.sock)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add -c: %v %s", err, out)
	}
	os.Remove(key)
	return a
}

// allow has the operator say yes to the next question.
func (a *confirmAgent) allow() { os.WriteFile(filepath.Join(a.dir, "allow"), nil, 0o644) }

// asked is how many times the agent asked the operator.
func (a *confirmAgent) asked() int {
	data, _ := os.ReadFile(filepath.Join(a.dir, "asked"))
	return strings.Count(string(data), "asked")
}

// The runner key lives only in ssh-agent, added with ssh-add -c. swim run
// asks for it once, as the run starts, to vouch for a run key it keeps in
// memory and signs every entry of the run with, the lane's round included.
// A lane that wants a signature of its own has no file to read and must
// ask the agent, which asks the operator (who says no); on Linux it can't
// read the launcher's memory either. swim verify trusts the run.
func TestRunKeyKeepsSigningFromLanes(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "zach")
	priv := keys["zach"]
	a := newConfirmAgent(t, priv)
	r.env = append(r.env, "SSH_AUTH_SOCK="+a.sock)
	r.mustSwim("config", "signing", "required")
	r.mustSwim("config", "signing_key", priv+".pub")
	out := filepath.Join(t.TempDir(), "lane")
	os.Mkdir(out, 0o755)
	r.script(1, "tries to sign", `echo forged > "`+out+`/msg"
if ssh-keygen -Y sign -q -n `+sign.Namespace+` -f "`+priv+`.pub" "`+out+`/msg" 2>/dev/null; then echo "agent signed" >> "`+out+`/result"; else echo "agent refused" >> "`+out+`/result"; fi
if cat "`+priv+`" >/dev/null 2>&1; then echo "key readable" >> "`+out+`/result"; fi
p=$PPID
while [ "${p:-1}" -gt 1 ]; do
  if [ "$(cat /proc/$p/comm 2>/dev/null)" = swim ]; then
    if cat /proc/$p/environ >/dev/null 2>&1; then echo "launcher readable" >> "`+out+`/result"; else echo "launcher hidden" >> "`+out+`/result"; fi
    break
  fi
  p=$(awk '{print $4}' /proc/$p/stat 2>/dev/null)
done
run "ok" true`)

	a.allow() // the operator says yes to swim run's one question
	r.mustSwim("run", "1", "--plain")
	data, _ := os.ReadFile(filepath.Join(out, "result"))
	result := string(data)
	if !strings.Contains(result, "agent refused") || strings.Contains(result, "agent signed") || strings.Contains(result, "key readable") {
		t.Errorf("the lane got a signature from the runner key:\n%s", result)
	}
	if runtime.GOOS == "linux" && os.Geteuid() != 0 && !strings.Contains(result, "launcher hidden") {
		t.Errorf("the lane could read the launcher's memory:\n%s", result)
	}
	if n := a.asked(); n != 2 {
		t.Errorf("the agent asked %d times, want 2 (swim run's run key, the lane's try)", n)
	}

	pub, _ := os.ReadFile(priv + ".pub")
	links, err := chain.Read(r.root)
	if err != nil || len(links) == 0 {
		t.Fatalf("chain: %v", err)
	}
	for _, l := range links {
		if l.RunKey == nil || sign.KeyOnly(l.SignedBy()) != sign.KeyOnly(string(pub)) || l.Signer == l.SignedBy() {
			t.Errorf("entry %d (%s) isn't signed by a run key the runner vouched for: signer %q, run key %+v", l.Seq, l.Type, l.Signer, l.RunKey)
		}
	}
	r.mustSwim("keys", "trust")
	contains(t, "verify", r.mustSwim("verify", "--levels"), "VERIFIED", "consistent  yes", "trusted     yes")
	if n := a.asked(); n != 2 {
		t.Errorf("verify asked the agent: %d", n)
	}

	// The operator says no: under signing: required the run stops before
	// any lane starts, asking nothing more; under auto it's recorded
	// unsigned, with one warning and no question per entry.
	before := len(links)
	if out, code := r.swim("run", "1", "--plain"); code == 0 || !strings.Contains(out, "didn't vouch for a run key") || strings.Contains(out, "[1] started") {
		t.Errorf("signing: required, the run key refused: exit %d\n%s", code, out)
	}
	if n := a.asked(); n != 3 {
		t.Errorf("asked %d times, want 3", n)
	}
	r.mustSwim("config", "signing", "auto")
	out2 := r.mustSwim("run", "1", "--plain")
	if strings.Count(out2, "recorded unsigned") != 1 {
		t.Errorf("signing: auto, the run key refused:\n%s", out2)
	}
	if n := a.asked(); n != 5 { // swim run's question, and the lane's try
		t.Errorf("asked %d times, want 5\n%s", n, out2)
	}
	links, _ = chain.Read(r.root)
	for _, l := range links[before:] {
		if l.Sig != "" {
			t.Errorf("entry %d signed after the operator said no", l.Seq)
		}
	}
	if len(links) == before {
		t.Error("the unsigned run recorded nothing")
	}
}
