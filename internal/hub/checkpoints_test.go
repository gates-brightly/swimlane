package hub

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// The hub keeps a worker's checkpoints append-only: a seq that doesn't go
// forward is refused (409), only the worker itself may add one, and with
// a state dir they survive a restart.
func TestHubCheckpointsGoForward(t *testing.T) {
	dir := t.TempDir()
	s, err := NewWithState("sekret", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	c := NewClient(srv.URL, "sekret", "")
	if _, err := c.Enroll(Worker{Name: "w", Host: "h", Root: "/r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(c.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.PutCheckpoint(chain.Checkpoint{V: 1, Seq: 5, Head: "aaa"}); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int{5, 3} {
		if err := c.PutCheckpoint(chain.Checkpoint{V: 1, Seq: seq, Head: "bbb"}); err == nil || !strings.Contains(err.Error(), "409") {
			t.Errorf("seq %d after 5: %v", seq, err)
		}
	}
	if err := c.PutCheckpoint(chain.Checkpoint{V: 1, Seq: 7, Head: "ccc"}); err != nil {
		t.Fatal(err)
	}
	// The enroll token isn't the worker's.
	other := NewClient(srv.URL, "sekret", c.ID)
	if err := other.PutCheckpoint(chain.Checkpoint{V: 1, Seq: 9, Head: "ddd"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("put with the enroll token: %v", err)
	}

	s2, err := NewWithState("sekret", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(s2.Handler())
	defer srv2.Close()
	c.URL = srv2.URL
	cps, err := c.Checkpoints()
	if err != nil || len(cps) != 2 || cps[1].Seq != 7 || cps[1].Head != "ccc" {
		t.Fatalf("after a restart: %+v %v", cps, err)
	}
	if err := c.PutCheckpoint(chain.Checkpoint{V: 1, Seq: 6, Head: "eee"}); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("seq 6 after a restart: %v", err)
	}
}
