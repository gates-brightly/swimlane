package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// rewriteChain renames the first round in r's chain and links every entry
// again, re-signing each with key (nil: left unsigned), as someone with
// the runner's key could.
func rewriteChain(t *testing.T, r *repo, key *sign.Key) {
	t.Helper()
	path := filepath.Join(r.root, ".swim", "chain.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	prev, renamed := "", false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var e chain.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "round" && !renamed {
			e.Round, renamed = "something nobody ran", true
		}
		e.Prev, e.Signer, e.Sig, e.RunKey = prev, "", "", nil
		if key != nil {
			e.Signer = key.Public
			payload, _ := json.Marshal(e)
			if e.Sig, err = sign.Sign(key, payload); err != nil {
				t.Fatal(err)
			}
		}
		b, _ := json.Marshal(e)
		out = append(out, append(b, '\n')...)
		prev = chain.Hash(b)
	}
	if !renamed {
		t.Fatal("no round to rename")
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// An unsigned chain proves nothing: swim verify says so, and a rewritten
// one fails the same way.
func TestVerifyRefusesUnsigned(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "one", `run "ok" true`)
	r.mustSwim("run", "1", "--plain")
	out, code := r.swim("verify")
	if code != 1 || !strings.Contains(out, "NOT VERIFIED") || !strings.Contains(out, "unsigned entries") {
		t.Fatalf("verify of an unsigned chain: exit %d\n%s", code, out)
	}
	contains(t, "allowed", r.mustSwim("verify", "--allow-unsigned"), "VERIFIED", "unsigned (accepted: --allow-unsigned)")
	rewriteChain(t, r, nil)
	if out, code := r.swim("verify"); code != 1 || !strings.Contains(out, "NOT VERIFIED") {
		t.Errorf("verify of a rewritten unsigned chain: exit %d\n%s", code, out)
	}
}

// A signed chain rewritten and re-signed with the runner's key, padded
// past the last checkpoint and checkpointed again, still fails: every
// checkpoint is checked, not only the newest.
func TestVerifyCatchesRewriteBehindCheckpoint(t *testing.T) {
	r, keys := signedRepo(t)
	dir := filepath.Join(t.TempDir(), "audit")
	r.write("swim.yml", "chain_anchors: [\"file:"+dir+"\"]\n", 0o644)
	r.mustSwim("run", "1")
	contains(t, "honest", r.mustSwim("verify"), "VERIFIED")

	key, err := sign.LoadKey(keys["zach"])
	if err != nil {
		t.Fatal(err)
	}
	rewriteChain(t, r, key)
	for i := 0; i < 3; i++ {
		r.mustSwim("run", "1") // past the last checkpoint, and a new one sent
	}
	r.mustSwim("chain", "checkpoint")
	out, code := r.swim("verify")
	if code != 1 || !strings.Contains(out, "NOT VERIFIED") || !strings.Contains(out, "chain replaced") {
		t.Errorf("verify of a re-signed rewrite: exit %d\n%s", code, out)
	}
}
