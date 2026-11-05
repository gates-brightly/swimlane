package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lint"
)

// probe runs name args with a short limit and returns its output.
func probe(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// bashVersion is the bash on PATH's "major.minor", or "" with none.
func bashVersion() string {
	if _, err := exec.LookPath("bash"); err != nil {
		return ""
	}
	out, err := probe("bash", "-c", `printf '%s.%s' "${BASH_VERSINFO[0]}" "${BASH_VERSINFO[1]}"`)
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(out)
}

// gnuTool asks each tool, once, whether it's GNU's (timeout: whether
// there is one).
func gnuTool() func(string) bool {
	seen := map[string]bool{}
	return func(tool string) bool {
		if v, ok := seen[tool]; ok {
			return v
		}
		var gnu bool
		if tool == "timeout" {
			_, err := exec.LookPath(tool)
			gnu = err == nil
		} else if out, err := probe(tool, "--version"); err == nil {
			gnu = strings.Contains(out, "GNU")
		}
		seen[tool] = gnu
		return gnu
	}
}

// laneFindings are lint's findings with the given code over every lane.
func (o *Options) laneFindings(code string) []finding.Finding {
	fs, err := lint.Only(o.Root, o.Cfg, code)
	if err != nil {
		return nil // swim lint says why a lane can't be read
	}
	return fs
}

// checkBash: lanes run with the bash on PATH. On a Mac that's bash 3.2,
// which only matters if a lane uses a bash 4 feature.
func checkBash(o *Options) []finding.Finding {
	if o.BashVersion == "" {
		return []finding.Finding{fd(finding.Error, "bash", "", "install bash", "bash is not on PATH; swim runs lane scripts with it")}
	}
	major, err := strconv.Atoi(strings.Split(o.BashVersion, ".")[0])
	if err != nil || major >= 4 {
		return nil
	}
	var out []finding.Finding
	for _, f := range o.laneFindings("bash4") {
		out = append(out, finding.Finding{Level: finding.Warn, Code: "bash", File: f.File, Line: f.Line,
			Message: fmt.Sprintf("%s, and the bash on PATH here is %s", strings.TrimSuffix(f.Message, "; macOS bash 3.2 can't run it"), o.BashVersion),
			Fix:     "rewrite it for bash 3.2, or put a bash 4+ first on PATH (brew install bash)"})
	}
	return out
}

// checkGNUOnly: a lane written on Linux that calls a GNU-only form (sed -i
// with no suffix, grep -P, ...) fails, or does something else, with the
// BSD tool a Mac has. Only where the tool here isn't GNU's.
func checkGNUOnly(o *Options) []finding.Finding {
	if strings.HasPrefix(o.Host, "linux/") {
		return nil
	}
	var out []finding.Finding
	for _, f := range o.laneFindings("gnu-only") {
		tool := lint.GNUOnlyTool(f)
		if tool == "" || o.GNU(tool) {
			continue
		}
		f.Message += " (swim lint warns too)"
		out = append(out, f)
	}
	return out
}
