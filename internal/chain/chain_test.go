package chain

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gates-brightly/swimlane/internal/sign"
)

func key(t *testing.T) *sign.Key {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "k")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", p).Run()
	k, err := sign.LoadKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestChainLinksAndDetectsTampering(t *testing.T) {
	root, k := t.TempDir(), key(t)
	for i := 0; i < 4; i++ {
		if _, err := Append(root, Entry{Type: "round", Lane: i + 1, Result: "passed"}, k); err != nil {
			t.Fatal(err)
		}
	}
	links, err := Read(root)
	if err != nil || len(links) != 4 || links[3].Seq != 4 || links[1].Prev != links[0].Hash {
		t.Fatalf("links: %+v %v", links, err)
	}
	if p, u := Check(links); len(p) != 0 || len(u) != 0 {
		t.Fatalf("clean chain: %+v %v", p, u)
	}

	data, _ := os.ReadFile(Path(root))
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	tamper := func(name string, ls []string, want string) {
		t.Helper()
		os.WriteFile(Path(root), []byte(strings.Join(ls, "\n")+"\n"), 0o644)
		links, _ := Read(root)
		p, _ := Check(links)
		if len(p) == 0 || !strings.Contains(p[0].What, want) {
			t.Errorf("%s: problems %+v, want %q", name, p, want)
		}
	}
	changed := append([]string(nil), lines...)
	changed[1] = strings.Replace(changed[1], `"passed"`, `"failed"`, 1)
	tamper("edit", changed, "signature doesn't match")
	tamper("remove", []string{lines[0], lines[2], lines[3]}, "seq 3 where 2")
	tamper("reorder", []string{lines[0], lines[2], lines[1], lines[3]}, "seq 3 where 2")
	// Re-signing an edited entry with another key still breaks the next link.
	os.WriteFile(Path(root), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func TestConcurrentAppends(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Append(root, Entry{Type: "approve", Lane: i}, nil); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	links, err := Read(root)
	if err != nil || len(links) != 30 {
		t.Fatalf("%d links, %v", len(links), err)
	}
	if p, u := Check(links); len(p) != 0 || len(u) != 30 {
		t.Fatalf("problems %+v unsigned %d", p, len(u))
	}
}

// bigEntry is a round entry whose line is over 300 KiB: more than the
// 256 KiB lastLine used to read back, and more than its first window.
func bigEntry(lane int) Entry {
	outs := map[string]string{}
	for i := 0; len(outs)*80 < 300*1024; i++ {
		outs[fmt.Sprintf("out-%05d.json", i)] = Hash([]byte{byte(i), byte(i >> 8)})
	}
	return Entry{Type: "round", Lane: lane, Result: "passed", Outputs: outs}
}

func TestAppendAfterLargeEntry(t *testing.T) {
	root, k := t.TempDir(), key(t)
	for i, e := range []Entry{{Type: "round", Lane: 1}, bigEntry(2), bigEntry(3), {Type: "round", Lane: 4}} {
		if _, err := Append(root, e, k); err != nil {
			t.Fatalf("append %d: %v", i+1, err)
		}
	}
	links, err := Read(root)
	if err != nil || len(links) != 4 || links[3].Seq != 4 || len(links[2].Outputs) == 0 {
		t.Fatalf("%d links, %v", len(links), err)
	}
	if st, _ := os.Stat(Path(root)); st.Size() < 600*1024 {
		t.Fatalf("chain is only %d bytes", st.Size())
	}
	if p, u := Check(links); len(p) != 0 || len(u) != 0 {
		t.Fatalf("problems %+v unsigned %v", p, u)
	}
}

func TestRepairIncompleteTail(t *testing.T) {
	root, k := t.TempDir(), key(t)
	for _, e := range []Entry{{Type: "round", Lane: 1}, bigEntry(2)} {
		if _, err := Append(root, e, k); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := Repair(root, k); err != nil || r.Bytes != 0 {
		t.Fatalf("clean chain repaired: %+v %v", r, err)
	}
	// A write cut short: half of a large third entry, no newline.
	half := []byte(`{"seq":3,"prev":"` + strings.Repeat("x", 200*1024))
	f, _ := os.OpenFile(Path(root), os.O_APPEND|os.O_WRONLY, 0)
	f.Write(half)
	f.Close()
	if _, err := Append(root, Entry{Type: "round", Lane: 3}, k); err == nil || !strings.Contains(err.Error(), "swim chain repair") {
		t.Fatalf("append after a cut-short write: %v", err)
	}
	if links, err := Read(root); err == nil || len(links) != 2 || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("read: %d links, %v", len(links), err)
	}

	r, err := Repair(root, k)
	if err != nil || r.Bytes != int64(len(half)) || r.SHA256 != Hash(half) {
		t.Fatalf("repair: %+v %v", r, err)
	}
	if kept, _ := os.ReadFile(r.Backup); string(kept) != string(half) {
		t.Fatalf("backup %s holds %d bytes", r.Backup, len(kept))
	}
	if _, err := Append(root, Entry{Type: "round", Lane: 3}, k); err != nil {
		t.Fatal(err)
	}
	links, err := Read(root)
	if err != nil || len(links) != 4 {
		t.Fatalf("%d links, %v", len(links), err)
	}
	rep := links[2]
	if rep.Type != "repair" || rep.Hash != r.Hash || rep.Outputs[filepath.Base(r.Backup)] != r.SHA256 || !strings.Contains(rep.Action, fmt.Sprint(len(half))) {
		t.Fatalf("repair entry: %+v", rep.Entry)
	}
	if p, u := Check(links); len(p) != 0 || len(u) != 0 {
		t.Fatalf("problems %+v unsigned %v", p, u)
	}
}

// A key that can't sign is a SignError and leaves the chain as it was, so
// the caller can record the repair unsigned instead.
func TestRepairSignError(t *testing.T) {
	root, k := t.TempDir(), key(t)
	if _, err := Append(root, Entry{Type: "round", Lane: 1}, k); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(Path(root), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":2,"prev":"`)
	f.Close()
	before, _ := os.ReadFile(Path(root))
	broken := *k
	broken.Path = filepath.Join(root, "no-such-key")
	_, err := Repair(root, &broken)
	if !errors.As(err, new(SignError)) {
		t.Fatalf("repair with a key that can't sign: %v", err)
	}
	if after, _ := os.ReadFile(Path(root)); string(after) != string(before) {
		t.Fatalf("the chain changed:\n%s", after)
	}
	if _, err := Append(root, Entry{Type: "round", Lane: 2}, &broken); err == nil {
		t.Fatal("append after the failed repair")
	}
	if r, err := Repair(root, nil); err != nil || r.Bytes == 0 {
		t.Fatalf("unsigned repair: %+v %v", r, err)
	}
}

func TestRepairWholeFileIncomplete(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Dir(Path(root)), 0o755)
	os.WriteFile(Path(root), []byte(`{"seq":1,"pr`), 0o644)
	if _, err := Repair(root, nil); err != nil {
		t.Fatal(err)
	}
	links, err := Read(root)
	if err != nil || len(links) != 1 || links[0].Seq != 1 || links[0].Prev != "" || links[0].Type != "repair" {
		t.Fatalf("%+v %v", links, err)
	}
}

// A chain replaced wholesale, every entry re-signed with a new key, is
// consistent (Check passes) but not trusted.
func TestTrustCatchesWholesaleReplacement(t *testing.T) {
	root, runner, mallory := t.TempDir(), key(t), key(t)
	for i := 0; i < 3; i++ {
		if _, err := Append(root, Entry{Type: "round", Lane: i + 1, Result: "passed"}, runner); err != nil {
			t.Fatal(err)
		}
	}
	links, _ := Read(root)
	trusted := func(pub string) bool { return sign.KeyOnly(pub) == sign.KeyOnly(runner.Public) }
	if u := Trust(links, trusted); len(u) != 0 {
		t.Fatalf("the runner's own chain: %+v", u)
	}
	os.Remove(Path(root))
	for _, l := range links {
		if _, err := Append(root, l.Entry, mallory); err != nil {
			t.Fatal(err)
		}
	}
	replaced, _ := Read(root)
	if p, u := Check(replaced); len(p) != 0 || len(u) != 0 {
		t.Fatalf("a re-signed chain should be consistent: %+v %v", p, u)
	}
	u := Trust(replaced, trusted)
	if len(u) != 3 || u[0].Seq != 1 || sign.KeyOnly(u[0].Key) != sign.KeyOnly(mallory.Public) {
		t.Fatalf("untrusted: %+v", u)
	}
}

// A checkpoint proves the chain's head: truncating the chain to before it,
// or replacing it, is caught; entries after it are only counted. A
// tampered checkpoint fails its signature.
func TestCheckpointCatchesTruncationAndReplacement(t *testing.T) {
	root, k := t.TempDir(), key(t)
	for i := 0; i < 4; i++ {
		Append(root, Entry{Type: "round", Lane: 1, Result: "passed"}, k)
	}
	links, _ := Read(root)
	cp := NewCheckpoint(links[:3], "host")
	if err := SignCheckpoint(&cp, k); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpoint(cp); err != nil {
		t.Fatalf("a fresh checkpoint: %v", err)
	}
	if r := CheckAgainst(links, cp); r.Status != Extends || r.Since != 1 {
		t.Errorf("one entry past the checkpoint: %+v", r)
	}
	// Truncated after the checkpoint: still extends it.
	if r := CheckAgainst(links[:3], cp); r.Status != Extends || r.Since != 0 {
		t.Errorf("truncated to the checkpoint: %+v", r)
	}
	if r := CheckAgainst(links[:2], cp); r.Status != Truncated {
		t.Errorf("truncated before the checkpoint: %+v", r)
	}
	// Truncated, then new entries appended: another entry at its seq.
	os.WriteFile(Path(root), nil, 0o644)
	for i := 0; i < 5; i++ {
		Append(root, Entry{Type: "round", Lane: 2, Result: "passed"}, k)
	}
	again, _ := Read(root)
	if r := CheckAgainst(again, cp); r.Status != Replaced {
		t.Errorf("replaced: %+v", r)
	}
	forged := cp
	forged.Seq = 2
	if err := VerifyCheckpoint(forged); err == nil {
		t.Error("a checkpoint changed after signing verified")
	}
	if err := VerifyCheckpoint(Checkpoint{Seq: 1, Head: "x"}); err != ErrUnsigned {
		t.Errorf("unsigned: %v", err)
	}
}
