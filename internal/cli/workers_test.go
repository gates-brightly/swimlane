package cli

import (
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/version"
)

// A worker can run a batch's built-in modules only if its swim has what
// .swim.lock pins; one that doesn't say which it has isn't judged.
func TestWorkerModules(t *testing.T) {
	sha := strings.Repeat("a", 64)
	lock := version.Lock{Version: "0.20261016.5", Modules: &version.LockModules{Builtin: map[string]version.LockedModule{
		"tf": {Version: "1.2.0", SHA256: sha},
	}}}
	worker := func(b map[string]version.LockedModule) *queue.Presence { return &queue.Presence{Builtins: b} }
	for _, c := range []struct {
		name string
		p    *queue.Presence
		want string
	}{
		{"same", worker(map[string]version.LockedModule{"tf": {Version: "1.2.0", SHA256: sha}}), ""},
		{"older", worker(map[string]version.LockedModule{"tf": {Version: "1.1.0", SHA256: strings.Repeat("b", 64)}}), "pins built-in tf 1.2.0, and this swim's is 1.1.0: update swim"},
		{"missing", worker(map[string]version.LockedModule{}), "its swim has no built-in tf"},
		{"an older worker", worker(nil), ""},
	} {
		if got := workerModules(lock, c.p, []string{"tf", "notify"}); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
