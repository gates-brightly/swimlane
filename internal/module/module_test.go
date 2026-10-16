package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
)

const notifySpec = `schema: swim.module/v1
name: notify
version: 2.1.0
kind: bash
summary: a repo notify
commands: [notify]
`

func writeModule(t *testing.T, root, dir, spec, src string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, SpecFile), []byte(spec), 0o644)
	if src != "" {
		name := strings.TrimPrefix(strings.SplitN(strings.SplitN(spec, "name: ", 2)[1], "\n", 2)[0], "")
		os.WriteFile(filepath.Join(d, name+".sh"), []byte(src), 0o644)
	}
}

func TestBuiltins(t *testing.T) {
	names := []string{}
	for _, m := range Builtins() {
		names = append(names, m.Name)
		if !m.Builtin || m.SHA256 == "" || m.Version == "" {
			t.Errorf("%s: %+v", m.Name, m)
		}
	}
	if got := strings.Join(names, " "); got != "aws-session backup claude clone notify smoke target tf wait" {
		t.Errorf("built-ins: %s", got)
	}
	// Built-in hashes are stable within a binary, and a Go module's covers
	// its source.
	a := Builtin("aws-session")
	if _, ok := a.Files["awssession.go"]; !ok {
		t.Errorf("aws-session's files: %v", a.Files)
	}
	if a.SHA256 != TreeHash(a.Files) {
		t.Error("hash")
	}
}

func TestResolve(t *testing.T) {
	root := t.TempDir()
	writeModule(t, root, "modules/notify", notifySpec, "notify() { :; }\n")
	writeModule(t, root, "modules/hello", strings.ReplaceAll(notifySpec, "notify", "hello"), "hello() { echo hi; }\n")

	// No policy: built-ins only.
	mods, err := Resolve(root, nil, []string{"clone", "notify@1"})
	if err != nil || len(mods) != 2 || !mods[1].Builtin || mods[1].Ref != "notify@1" {
		t.Fatalf("built-ins: %v %v", mods, err)
	}
	if _, err := Resolve(root, nil, []string{"./modules/hello"}); err == nil || !strings.Contains(err.Error(), "add ./modules/hello to swim.yml modules: allow:") {
		t.Errorf("a repo module without a policy: %v", err)
	}
	if _, err := Resolve(root, nil, []string{"notify@2"}); err == nil || !strings.Contains(err.Error(), "not major 2") {
		t.Errorf("a major the built-in isn't: %v", err)
	}
	if _, err := Resolve(root, nil, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "no such module") {
		t.Errorf("unknown: %v", err)
	}
	if _, err := Resolve(root, nil, []string{"Bad!"}); err == nil {
		t.Error("a bad name resolved")
	}

	pol := &config.ModulesPolicy{Allow: []string{"clone", "./modules/*"}, Dir: "modules"}
	// A policy that lists modules allows only those.
	if _, err := Resolve(root, pol, []string{"aws-session"}); err == nil || !strings.Contains(err.Error(), "isn't allowed") {
		t.Errorf("a built-in the policy leaves out: %v", err)
	}
	mods, err = Resolve(root, pol, []string{"hello", "./modules/hello@2"})
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("twice: %v %v", mods, err)
	}
	mods, err = Resolve(root, pol, []string{"hello"})
	if err != nil || mods[0].Builtin || mods[0].Dir != "modules/hello" || mods[0].Source() != "./modules/hello" {
		t.Fatalf("by name from dir: %+v %v", mods, err)
	}
	// A repo module shadows a built-in only with override.
	if _, err := Resolve(root, pol, []string{"notify"}); err == nil || !strings.Contains(err.Error(), "would shadow the built-in notify") {
		t.Errorf("shadow: %v", err)
	}
	pol.Override = []string{"notify"}
	mods, err = Resolve(root, pol, []string{"notify@2"})
	if err != nil || mods[0].Builtin {
		t.Errorf("override: %v %v", mods, err)
	}
	// Editing a file changes the module's hash.
	before := mods[0].SHA256
	os.WriteFile(filepath.Join(root, "modules/notify/notify.sh"), []byte("notify() { echo changed; }\n"), 0o644)
	mods, _ = Resolve(root, pol, []string{"notify"})
	if mods[0].SHA256 == before {
		t.Error("an edit kept the hash")
	}
	// FromStage checks the pin.
	if _, err := FromStage(root, "modules/notify", before); err == nil {
		t.Error("a stale pin passed")
	}
	if m, err := FromStage(root, "modules/notify", mods[0].SHA256); err != nil || m.Name != "notify" {
		t.Errorf("FromStage: %v", err)
	}
}

func TestSpec(t *testing.T) {
	for _, c := range []struct{ spec, want string }{
		{"schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: bash\ncommands: [x]\n", ""},
		{"schema: swim.module/v1\nname: x\nversion: 1.0\nkind: bash\ncommands: [x]\n", "MAJOR.MINOR.PATCH"},
		{"schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: python\ncommands: [x]\n", "want bash or go"},
		{"schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: bash\ncommands: [y]\n", "isn't x or x_<something>"},
		{"schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: bash\n", "no commands"},
		{"schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: bash\ncommands: [x]\neffects: {sockets: true}\n", "sockets"},
		{"schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: go\ncommands: [x, x_y]\n", "one program"},
		{"schema: other\nname: x\nversion: 1.0.0\nkind: bash\ncommands: [x]\n", "schema"},
	} {
		_, err := ParseSpec([]byte(c.spec))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q: %v", c.spec, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q: %v, want %q", c.spec, err, c.want)
		}
	}
	// A command module.yml lists that the bash file doesn't define.
	_, err := New(map[string][]byte{SpecFile: []byte("schema: swim.module/v1\nname: x\nversion: 1.0.0\nkind: bash\ncommands: [x, x_more]\n"), "x.sh": []byte("x() { :; }\n")})
	if err == nil || !strings.Contains(err.Error(), "doesn't define x_more") {
		t.Errorf("undefined command: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	for _, c := range []struct {
		in         string
		name, path string
		major      int
		bad        bool
	}{
		{in: "notify", name: "notify", major: -1},
		{in: "clone@1", name: "clone", major: 1},
		{in: "./modules/x", path: "modules/x", major: -1},
		{in: "./modules/x@3", path: "modules/x", major: 3},
		{in: "./../x", bad: true},
		{in: "x@one", bad: true},
		{in: "X", bad: true},
	} {
		r, err := ParseRef(c.in)
		if c.bad != (err != nil) || (!c.bad && (r.Name != c.name || r.Path != c.path || r.Major != c.major)) {
			t.Errorf("%s: %+v %v", c.in, r, err)
		}
	}
}
