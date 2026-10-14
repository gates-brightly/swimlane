package anchor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

func TestFileAnchorAppendsOnly(t *testing.T) {
	root, dir := t.TempDir(), filepath.Join(t.TempDir(), "audit")
	if _, err := Open("file:"+filepath.Join(root, "x"), Options{Root: root}); err == nil {
		t.Error("an anchor inside the repo was opened")
	}
	if _, err := Open("file:relative", Options{Root: root}); err == nil {
		t.Error("a relative anchor was opened")
	}
	a, err := Open("file:"+dir, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		chain.Append(root, chain.Entry{Type: "round", Lane: 1}, nil)
	}
	if n, err := Export(a, chain.Path(root)); err != nil || n != 3 {
		t.Fatalf("export: %d %v", n, err)
	}
	chain.Append(root, chain.Entry{Type: "round", Lane: 2}, nil)
	if n, err := Export(a, chain.Path(root)); err != nil || n != 1 {
		t.Fatalf("export again: %d %v", n, err)
	}
	links, _ := chain.Read(root)
	cp := chain.NewCheckpoint(links, "h")
	if err := a.PutCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	if err := a.PutCheckpoint(cp); err != nil {
		t.Errorf("the same checkpoint again: %v", err)
	}
	if got, err := a.Latest(); err != nil || got == nil || got.Seq != 4 || got.Head != cp.Head {
		t.Fatalf("latest: %+v %v", got, err)
	}
	if err := a.PutCheckpoint(chain.NewCheckpoint(links[:2], "h")); !errors.Is(err, ErrBehind) {
		t.Errorf("an older checkpoint: %v", err)
	}

	// The local chain replaced: export refuses and writes nothing.
	orig, _ := os.ReadFile(chain.Path(root))
	os.Remove(chain.Path(root))
	for i := 0; i < 5; i++ {
		chain.Append(root, chain.Entry{Type: "round", Lane: 9}, nil)
	}
	if _, err := Export(a, chain.Path(root)); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Errorf("export of a replaced chain: %v", err)
	}
	// Cut back to its first two entries: refused too.
	ls := strings.SplitAfter(string(orig), "\n")
	os.WriteFile(chain.Path(root), []byte(ls[0]+ls[1]), 0o644)
	if _, err := Export(a, chain.Path(root)); err == nil || !strings.Contains(err.Error(), "lost entries") {
		t.Errorf("export of a truncated chain: %v", err)
	}
	if theirs, _ := a.Entries(); len(theirs) != 4 {
		t.Errorf("the anchor holds %d entries, want the 4 exported", len(theirs))
	}
}
