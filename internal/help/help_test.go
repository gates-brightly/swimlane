package help

import (
	"regexp"
	"strings"
	"testing"

	"swim/internal/assets"
)

// Every function the lane script library defines must be documented in the guide,
// and every command must have detail and appear in the command list.
func TestGuideCoversLibAndCommands(t *testing.T) {
	fnRE := regexp.MustCompile(`(?m)^([a-z][a-z_]*)\(\) \{`)
	fns := fnRE.FindAllStringSubmatch(assets.Lib, -1)
	if len(fns) < 10 {
		t.Fatalf("found only %d lib functions", len(fns))
	}
	section := Guide[strings.Index(Guide, "WRITING A LANE SCRIPT"):strings.Index(Guide, "SAFETY RULES")]
	for _, m := range fns {
		if !strings.Contains(section, "  "+m[1]) {
			t.Errorf("lib function %s is not documented under WRITING A LANE SCRIPT", m[1])
		}
	}
	commands := Guide[strings.Index(Guide, "COMMANDS"):strings.Index(Guide, "CONCEPTS")]
	for _, name := range Names() {
		if !strings.Contains(commands, "  "+name) {
			t.Errorf("command %s missing from COMMANDS", name)
		}
		if !strings.HasPrefix(Commands[name], "swim "+name) {
			t.Errorf("command %s detail should start with its usage", name)
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
