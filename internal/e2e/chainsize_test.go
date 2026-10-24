package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// A chain entry over 256 KiB (a round with thousands of outputs) used to
// stop every later append: the next round still records, and verify
// passes. A write cut short leaves a half line that swim chain repair
// removes, recorded in the chain.
func TestChainLargeEntryAndRepair(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", keys["zach"])
	r.script(1, "small", `run "ok" true`)
	r.mustSwim("run", "1")

	// The big round goes in through the package, signed by the runner's
	// key, as the launcher would record it.
	k, err := sign.LoadKey(keys["zach"])
	if err != nil {
		t.Fatal(err)
	}
	outs := map[string]string{}
	for i := 0; len(outs)*90 < 300*1024; i++ {
		outs[fmt.Sprintf("shard-%05d/stats.json", i)] = chain.Hash([]byte(fmt.Sprint(i)))
	}
	if _, err := chain.Append(r.root, chain.Entry{Type: "round", Lane: 2, Result: "passed", Outputs: outs}, k); err != nil {
		t.Fatal(err)
	}
	r.mustSwim("run", "1")
	links, err := chain.Read(r.root)
	// Each run: its round, then swim run's attestation.
	if err != nil || len(links) != 5 || links[3].Type != "round" || links[3].Lane != 1 || links[4].Type != "run" {
		t.Fatalf("the round after the big entry wasn't recorded: %d links, %v", len(links), err)
	}
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "5 entries", "consistent  yes")

	// A write cut short: half an entry, no newline.
	f, _ := os.OpenFile(chain.Path(r.root), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":4,"prev":"`)
	f.Close()
	if out, code := r.swim("verify", "--levels", "--allow-unsigned"); code != 1 || !strings.Contains(out, "incomplete entry") || !strings.Contains(out, "swim chain repair") {
		t.Fatalf("verify with a half line: %d %s", code, out)
	}
	contains(t, "run with a half line", r.mustSwim("run", "1"), "the last entry is incomplete", "swim chain repair")

	out := r.mustSwim("chain", "repair")
	contains(t, "repair", out, "removed 17 bytes of an incomplete final entry", "kept in .swim/chain.tail-")
	baks, _ := filepath.Glob(filepath.Join(r.root, ".swim", "chain.tail-*.bak"))
	if len(baks) != 1 {
		t.Fatalf("backups: %v", baks)
	}
	if data, _ := os.ReadFile(baks[0]); string(data) != `{"seq":4,"prev":"` {
		t.Fatalf("backup holds %q", data)
	}
	contains(t, "repair again", r.mustSwim("chain", "repair"), "nothing to repair")
	r.mustSwim("run", "1")
	contains(t, "verify after repair", r.mustSwim("verify", "--levels", "--allow-unsigned"), "8 entries", "note: entry #6: swim chain repair removed 17 bytes", "consistent  yes")
	contains(t, "history", r.mustSwim("history", "--oneline", "-n", "3"), "repair: removed 17 bytes of an incomplete final entry")
}
