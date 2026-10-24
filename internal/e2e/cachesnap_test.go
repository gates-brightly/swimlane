package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/chain"
)

// A snapshot (# Cache: snap:TREE @files): a miss installs and saves; the
// next round, from a fresh tree, restores it byte for byte (links too) and
// its chain entry names it; a changed key file is a miss again; a key is
// written once; swim cache ls and rm see it.
func TestCacheSnapshot(t *testing.T) {
	r, _ := cacheRepo(t)
	os.MkdirAll(filepath.Join(r.root, "web"), 0o755)
	r.write("web/package-lock.json", `{"lockfileVersion": 3, "v": 1}`+"\n", 0o644)
	r.script(1, "install with a snapshot", `# Cache: snap:web/node_modules @web/package-lock.json
cache restore web/node_modules || { run "install" bash -c 'mkdir -p web/node_modules/a && echo installed > web/node_modules/a/x && ln -s x web/node_modules/a/link'; cache save web/node_modules; }
run "use" cat web/node_modules/a/link
run "a link" test -L web/node_modules/a/link
cache save web/node_modules`)
	contains(t, "lint", must(r.swim("lint", "1")), "")

	r.mustSwim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "round 1", log, "PASS  cache restore web/node_modules (miss)", "PASS  install", "PASS  cache save web/node_modules (saved ", "| installed", "PASS  cache save web/node_modules (already saved ")

	// The next round's staged copy is fresh: restored, the link a link.
	r.mustSwim("run", "1", "--plain")
	log = r.log(1)
	rounds := strings.Split(log, "== ROUND")
	last := rounds[len(rounds)-1]
	contains(t, "round 2", last, "PASS  cache restore web/node_modules (hit ", "restored 1 file (10 B)", "| installed", "PASS  a link")
	if strings.Contains(last, "PASS  install") {
		t.Errorf("round 2 installed again:\n%s", last)
	}
	links, _ := chain.Read(r.root)
	var refs []chain.SnapRef
	for _, l := range links {
		if l.Type == "round" && l.Lane == 1 {
			refs = l.Cache
		}
	}
	if len(refs) != 1 || refs[0].Tree != "web/node_modules" || refs[0].Key == "" || refs[0].TreeSHA256 == "" {
		t.Errorf("round 2's chain entry: %+v", refs)
	}

	// The key file changes: a miss.
	r.write("web/package-lock.json", `{"lockfileVersion": 3, "v": 2}`+"\n", 0o644)
	r.mustSwim("run", "1", "--plain")
	rounds = strings.Split(r.log(1), "== ROUND")
	contains(t, "round 3", rounds[len(rounds)-1], "(miss)", "PASS  install")

	contains(t, "cache ls", r.mustSwim("cache", "ls"), "snap.web_node_modules")
	r.mustSwim("cache", "rm", "snap.web_node_modules")
	if strings.Contains(r.mustSwim("cache", "ls"), "snap.web_node_modules") {
		t.Error("cache rm left the snapshots")
	}
	contains(t, "verify", r.mustSwim("verify", "--levels", "--allow-unsigned"), "consistent  yes")
}

// A save from a round with failed steps is marked, and restoring it says
// so.
func TestCacheSnapshotRules(t *testing.T) {
	r, _ := cacheRepo(t)
	r.write("deps.txt", "a\n", 0o644)
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: snapshot rules
# Cache: snap:vendor @deps.txt
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
run "broken" false
run "vendor" bash -c 'mkdir -p vendor && echo v > vendor/f'
cache save vendor
summary
`, 0o755)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "rules", log, "PASS  cache save vendor (saved ", ", from a failed round)")
	r.write("lane.2.sh", `#!/usr/bin/env bash
# swim: syntax 2
# Round: restore it
# Cache: snap:vendor @deps.txt
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2
cache restore vendor
summary
`, 0o755)
	r.mustSwim("run", "2", "--plain")
	contains(t, "restore", r.log(2), "PASS  cache restore vendor (hit ", "saved by a failed round", "-- saved by a round that had failed steps")
	if out, code := r.swim("lint", "2"); code != 0 {
		t.Errorf("lint: %s", out)
	}
}
