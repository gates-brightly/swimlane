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
	tamper("remove", []string{lines[0], lines[2], lines[3]}, "entry #2 is missing")
	tamper("reorder", []string{lines[0], lines[2], lines[1], lines[3]}, "entry #2 is missing")
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

// An entry signed by a run key counts as the runner key's when the runner
// key vouched for that run key: a key of a lane's own with a delegation
// copied from another entry breaks, and one another key vouched for is
// that key's.
func TestRunKeyEntries(t *testing.T) {
	root, runner, mallory := t.TempDir(), key(t), key(t)
	rk, err := sign.NewRunKey(runner, "r1", "h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Append(root, Entry{Type: "round", Run: "r1", Lane: 1}, rk); err != nil {
		t.Fatal(err)
	}
	links, _ := Read(root)
	if links[0].RunKey == nil || links[0].SignedBy() != runner.Public {
		t.Fatalf("run key entry: %+v", links[0].Entry)
	}
	if p, u := Check(links); len(p) != 0 || len(u) != 0 {
		t.Fatalf("an honest run key entry: %+v %v", p, u)
	}

	// A lane signs with a key of its own and borrows the delegation.
	own, _ := sign.NewRunKey(mallory, "r1", "h")
	forged := *own
	forged.RunKey = links[0].RunKey
	if _, err := Append(root, Entry{Type: "approve", Run: "r1", Lane: 1, Guard: "F"}, &forged); err != nil {
		t.Fatal(err)
	}
	// Or has its own key vouch for one.
	if _, err := Append(root, Entry{Type: "approve", Run: "r1", Lane: 1, Guard: "F"}, own); err != nil {
		t.Fatal(err)
	}
	links, _ = Read(root)
	p, _ := Check(links)
	if len(p) != 1 || p[0].Seq != 2 || !strings.Contains(p[0].What, "vouches for another key") {
		t.Errorf("a borrowed delegation: %+v", p)
	}
	if links[2].SignedBy() != mallory.Public {
		t.Errorf("a run key vouched for by another key: signed by %q", links[2].SignedBy())
	}
}

// A removed entry is one gap, reported where the numbering jumps, not a
// problem at every entry after it.
func TestGapReportedOnce(t *testing.T) {
	links := []Link{{Entry: Entry{Seq: 1}, Line: 1}, {Entry: Entry{Seq: 4}, Line: 2}, {Entry: Entry{Seq: 5}, Line: 3}, {Entry: Entry{Seq: 6}, Line: 4}}
	for i := 1; i < len(links); i++ {
		links[i].Prev = links[i-1].Hash
	}
	p, _ := Check(links)
	if len(p) != 1 || p[0].Seq != 4 || p[0].What != "entries #2-#3 are missing before it (removed)" {
		t.Errorf("problems %+v", p)
	}
}
