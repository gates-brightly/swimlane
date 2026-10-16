package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
)

const lane3 = `#!/usr/bin/env bash
# Round: drop orders
# Includes: lib/inc.sh lib/orders.sh lib/deep.sh
# Guards: DROP  drop the table
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3
source lib/orders.sh
source lib/undeclared.sh
if guard DROP "drop orders"; then run "drop" true; fi
summary
`

// repo writes a repo with lane 3, what it includes and sources, a sidecar,
// swim.yml and lookup.yml.
func repo(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	files := map[string]string{
		"lane.3.sh":         lane3,
		"lib/inc.sh":        "inc=1\n",
		"lib/orders.sh":     ". lib/deep.sh\n",
		"lib/deep.sh":       "deep=1\n",
		"lib/undeclared.sh": "undeclared=1\n",
		"lane.3.yml":        "owner: ops\n",
		"data/rows.csv":     "a,b\n",
		"swim.yml":          "blocked_commands: [\"rm -rf /\"]\nmax_parallel: 2\n",
		"lookup.yml":        "users: {}\nrequire:\n  - guards: [DROP]\n    count: 1\n",
		"lanes.ci.sh":       "#!/usr/bin/env bash\n# Round: ci\n\n# swim job 5.1\n# Round: fmt\nrun \"fmt\" true\n\n# swim job 5.2\n# Round: test\n# Guards: SHIP  ship it\nif guard SHIP \"ship\"; then run \"ship\" true; fi\n",
		"notes/other.md":    "not code\n",
	}
	for p, data := range files {
		write(t, root, p, data)
	}
	return root
}

func write(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	mode := os.FileMode(0o644)
	if strings.HasSuffix(rel, ".sh") && !strings.HasPrefix(rel, "lib/") {
		mode = 0o755
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, root string, n int) *Manifest {
	t.Helper()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Build(root, cfg, n, "env-a")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBuildPins(t *testing.T) {
	root := repo(t)
	m := build(t, root, 3)
	var inc []string
	for _, f := range m.Pinned.Includes {
		inc = append(inc, f.Path+":"+f.From)
	}
	if got := strings.Join(inc, " "); got != "lib/deep.sh:includes lib/inc.sh:includes lib/orders.sh:includes" {
		t.Errorf("includes: %s", got)
	}
	if m.Pinned.Sidecar == nil || m.Pinned.Sidecar.Path != "lane.3.yml" || m.Pinned.Runner != "bash" || m.Pinned.Config.Rules == "" {
		t.Errorf("pinned: %+v", m.Pinned)
	}
	if strings.Join(m.Guards, ",") != "DROP" || m.Local.Env != "env-a" {
		t.Errorf("guards %v, env %q", m.Guards, m.Local.Env)
	}
	// The script hash approvals have always named is unchanged.
	data, _ := lane.ScriptBytes(root, 3)
	if m.ScriptSHA != hash(data) {
		t.Error("ScriptSHA isn't lane.ScriptBytes' hash")
	}
}

// Changing each pinned input changes manifest_sha256 (and Diff says
// which); changing anything else doesn't.
func TestCoverage(t *testing.T) {
	cases := []struct {
		name, file, data string
		changes          bool
		why              string
	}{
		{"script", "lane.3.sh", lane3 + "# more\n", true, "script lane.3.sh changed"},
		{"declared include", "lib/inc.sh", "inc=2\n", true, "includes lib/inc.sh changed"},
		{"sourced file", "lib/orders.sh", ". lib/deep.sh\necho changed\n", true, "includes lib/orders.sh changed"},
		{"file it sources", "lib/deep.sh", "deep=2\n", true, "includes lib/deep.sh changed"},
		{"sidecar", "lane.3.yml", "owner: someone\n", true, "sidecar lane.3.yml changed"},
		{"swim.yml policy", "swim.yml", "blocked_commands: [\"rm -rf /\", \"curl\"]\nmax_parallel: 2\n", true, "config policy changed (swim.yml: blocked_commands"},
		{"lookup.yml rules", "lookup.yml", "users: {}\nrequire:\n  - guards: [DROP]\n    count: 2\n", true, "approval rules changed (lookup.yml require:)"},
		{"swim.yml other key", "swim.yml", "blocked_commands: [\"rm -rf /\"]\nmax_parallel: 5\n", false, ""},
		{"lookup.yml users", "lookup.yml", "users: {bob: {keys: []}}\nrequire:\n  - guards: [DROP]\n    count: 1\n", false, ""},
		{"a data file", "data/rows.csv", "c,d\n", false, ""},
		// Only what the header names is pinned: a file the script sources
		// without # Includes: is refused when the lane runs, not pinned.
		{"a file sourced but not declared", "lib/undeclared.sh", "undeclared=2\n", false, ""},
		{"another lane", "lanes.ci.sh", "#!/usr/bin/env bash\n# Round: ci\n\n# swim job 5.1\n# Round: fmt2\nrun \"fmt\" true\n", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t)
			before := build(t, root, 3)
			write(t, root, c.file, c.data)
			after := build(t, root, 3)
			if changed := after.SHA256 != before.SHA256; changed != c.changes {
				t.Fatalf("manifest changed = %v, want %v", changed, c.changes)
			}
			why := strings.Join(Diff(before.Pinned, after.Pinned), "; ")
			if c.why != "" && !strings.Contains(why, c.why) {
				t.Errorf("Diff = %q, want %q", why, c.why)
			}
		})
	}
	// The env files' values differ per machine: pinned for the run, not in
	// the hash approvals sign.
	root := repo(t)
	cfg, _ := config.Load(root)
	a, _ := Build(root, cfg, 3, "env-a")
	b, _ := Build(root, cfg, 3, "env-b")
	if a.SHA256 != b.SHA256 || a.Local.Env == b.Local.Env {
		t.Error("env should be a local pin, outside manifest_sha256")
	}
}

// The stage holds read-only copies; Verify catches a changed one, and the
// staged copy rebuilds the script hash.
func TestStageAndVerify(t *testing.T) {
	root := repo(t)
	m := build(t, root, 3)
	dir, err := m.Stage(root, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, ".swim", "runs", "r-1", "3") {
		t.Errorf("stage dir %s", dir)
	}
	st, err := os.Stat(filepath.Join(dir, "lib", "orders.sh"))
	if err != nil || st.Mode().Perm() != 0o400 {
		t.Fatalf("staged include: %v %v", st, err)
	}
	if err := m.Verify(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.SHA256 != m.SHA256 || loaded.Verify() != nil {
		t.Fatalf("load: %+v %v", loaded, err)
	}
	data, _ := lane.ScriptBytes(root, 3)
	if got, _ := loaded.ScriptBytes(); string(got) != string(data) {
		t.Error("the staged copy's script bytes differ from the repo's")
	}
	// The working tree moves on; the stage doesn't.
	write(t, root, "lib/orders.sh", "echo edited\n")
	if err := loaded.Verify(); err != nil {
		t.Errorf("an edit to the working tree failed the stage: %v", err)
	}
	p := filepath.Join(dir, "lib", "orders.sh")
	os.Chmod(p, 0o600)
	os.WriteFile(p, []byte("echo tampered\n"), 0o600)
	if err := loaded.Verify(); err == nil || !strings.Contains(err.Error(), "includes lib/orders.sh") {
		t.Errorf("tampered stage: %v", err)
	}
	// Staging the same run again replaces it; removing works on read-only files.
	if _, err := m.Stage(root, "r-1"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(filepath.Join(root, ".swim", "runs", "r-1")); err != nil {
		t.Fatal(err)
	}
}

// A bash lane of jobs stages the script swim generates for each job.
func TestStageJobs(t *testing.T) {
	root := repo(t)
	m := build(t, root, 5)
	if m.Pinned.Script.Path != "lanes.ci.sh" || strings.Join(m.Guards, ",") != "SHIP" {
		t.Fatalf("group manifest: %+v %v", m.Pinned.Script, m.Guards)
	}
	if _, err := m.Stage(root, "r-2"); err != nil {
		t.Fatal(err)
	}
	p, ok := m.JobScript("2")
	if !ok {
		t.Fatal("no staged script for job 5.2")
	}
	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), "lane_init 5") || !strings.Contains(string(data), `guard SHIP`) {
		t.Fatalf("job script: %v\n%s", err, data)
	}
	if !strings.HasPrefix(p, filepath.Join(root, ".swim", "runs", "r-2", "5", ".swim", "groups")) {
		t.Errorf("job script at %s", p)
	}
}

func TestObserve(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "fact"), []byte("one\n"), 0o644)
	p1, err := lane.ParseProbe("acct = cat fact")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := lane.ParseProbe("path sh nosuchcommand-xyz")
	if err != nil {
		t.Fatal(err)
	}
	was := Observe(root, []lane.Probe{p1, p2}, nil)
	if was["acct"] != "one" || !strings.Contains(was["path.sh"], "@sha256:") || was["path.nosuchcommand-xyz"] != "(not found)" {
		t.Errorf("observed: %v", was)
	}
	os.WriteFile(filepath.Join(root, "fact"), []byte("two\n"), 0o644)
	diff := ObservedDiff(was, Observe(root, []lane.Probe{p1, p2}, nil))
	if len(diff) != 1 || diff[0] != "acct was one at approval, now two" {
		t.Errorf("diff: %v", diff)
	}
	if _, err := lane.ParseProbe("not a probe"); err == nil {
		t.Error("a bad probe parsed")
	}
}
