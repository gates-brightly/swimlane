package help

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane"
	"github.com/gates-brightly/swimlane/internal/assets"
)

// Every function the lane script library defines is documented in the
// lanes topic, and every command is in the commands topic and has detail.
func TestGuideCoversLibAndCommands(t *testing.T) {
	fnRE := regexp.MustCompile(`(?m)^([a-z][a-z_]*)\(\) \{`)
	fns := fnRE.FindAllStringSubmatch(assets.Lib, -1)
	if len(fns) < 10 {
		t.Fatalf("found only %d lib functions", len(fns))
	}
	lanes, _ := Topic("lanes")
	for _, m := range fns {
		if !strings.Contains(lanes, "`"+m[1]) {
			t.Errorf("lib function %s is not documented in the lanes topic", m[1])
		}
	}
	commands, _ := Topic("commands")
	for _, name := range Names() {
		if name != "help" && !regexp.MustCompile("`"+name+"\\b").MatchString(commands) {
			t.Errorf("command %s missing from the commands topic", name)
		}
		if !strings.HasPrefix(Commands[name], "swim "+name) {
			t.Errorf("command %s detail should start with its usage", name)
		}
	}
}

// Every page in docs/reference is a topic: the index links it.
func TestEveryReferencePageIsATopic(t *testing.T) {
	pages, err := fs.Glob(swimlane.Reference, "docs/reference/*.md")
	if err != nil || len(pages) < 10 {
		t.Fatalf("found %d reference pages: %v", len(pages), err)
	}
	topics := map[string]bool{}
	for _, name := range Topics {
		topics[name] = true
	}
	for _, p := range pages {
		name := strings.TrimSuffix(path.Base(p), ".md")
		if name != "README" && !topics[name] {
			t.Errorf("docs/reference/README.md doesn't link %s.md", name)
		}
	}
}

// The library must stay runnable on macOS bash 3.2.
func TestLibAvoidsBash4Features(t *testing.T) {
	var code strings.Builder
	for _, line := range strings.Split(assets.Lib, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			code.WriteString(line + "\n")
		}
	}
	for _, bad := range []string{"declare -A", "local -A", "mapfile", "readarray", ",,}", "^^}", "[-1]", "&>>", "|&", "local -n", "${!", "coproc"} {
		if strings.Contains(code.String(), bad) {
			t.Errorf("lib.sh uses %q, which macOS bash 3.2 lacks", bad)
		}
	}
}

func TestFullHelpHasEveryCommand(t *testing.T) {
	in := map[string]bool{}
	for _, c := range FullOrder {
		if in[c] {
			t.Errorf("%s twice in FullOrder", c)
		}
		in[c] = true
		if _, ok := Commands[c]; !ok {
			t.Errorf("FullOrder has %s, which has no help", c)
		}
	}
	for c := range Commands {
		if !in[c] && c != "help" { // swim help's own entry: the guide is its output
			t.Errorf("swim --help leaves out %s: add it to FullOrder", c)
		}
	}
}

// swim --help on a terminal is a short overview; the guide holds the rest.
func TestShortHelpIsShort(t *testing.T) {
	short := Short()
	if n := strings.Count(short, "\n"); n >= 80 {
		t.Errorf("Short() is %d lines; keep it under 80", n)
	}
	for _, c := range []string{"init", "new", "run", "approve", "verify", "queue", "watch", "swim guide"} {
		if !strings.Contains(short, c) {
			t.Errorf("Short() leaves out %s", c)
		}
	}
}
