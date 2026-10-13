package chain

import (
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
