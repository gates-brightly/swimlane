// Package runner decides how a lane script runs: bash for lane.N.sh, the
// script's #! line, a runner the operator confirmed (config runners:, or
// the repo's swim.yml), or the program itself for a compiled lane.
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// Where a plan's runner came from.
const (
	Builtin = "builtin" // bash for .sh
	Shebang = "shebang" // the script's #! line
	Config  = "config"  // runners: in ~/.config/swim/config.yml
	RepoYML = "swim.yml"
	Direct  = "direct" // an executable (binary) run as itself
)

// Plan is how one lane script runs.
type Plan struct {
	Script string   // the script's path
	Argv   []string // the command: runner words, then the script
	Source string   // Builtin, Shebang, Config, RepoYML or Direct
	// Wrapped: the script doesn't load swim's bash library (lane_init), so
	// it runs inside `swim _wrap`, which opens and closes its round.
	Wrapped bool
}

// Runner is the plan's runner as people read it: "bash", "python3 (#!)",
// "tsx (config)", "./lane.3 (direct)".
func (p Plan) Runner() string {
	if p.Source == Direct {
		return "./" + filepath.Base(p.Script) + " (direct)"
	}
	words := strings.Join(p.Argv[:len(p.Argv)-1], " ")
	switch p.Source {
	case Shebang:
		return words + " (#!)"
	case Builtin:
		return words
	}
	return words + " (" + p.Source + ")"
}

// Candidates are the commands swim looks for on PATH, in order, when it
// meets an extension it has no runner for.
var Candidates = map[string][]string{
	"js":  {"node", "bun", "deno run -A"},
	"mjs": {"node", "bun", "deno run -A"},
	"cjs": {"node", "bun"},
	"ts":  {"tsx", "ts-node", "bun", "deno run -A"},
	"mts": {"tsx", "ts-node", "bun", "deno run -A"},
	"py":  {"python3", "python"},
	"rb":  {"ruby"},
	"pl":  {"perl"},
	"php": {"php"},
	"lua": {"lua"},
	"go":  {"go run"},
	"zsh": {"zsh"},
}

// NeedRunner is the error for a script swim has no runner for yet: no #!
// line, no runners: entry for its extension, and not executable.
type NeedRunner struct {
	Script string
	Ext    string
	Found  []string // candidates on PATH, resolved ("/usr/local/bin/tsx")
	Tried  []string // every candidate looked for
}

func (e *NeedRunner) Error() string {
	name := filepath.Base(e.Script)
	ex := "tsx"
	if len(e.Found) > 0 {
		ex = e.Found[0]
	} else if c := Candidates[e.Ext]; len(c) > 0 {
		ex = c[0]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: no runner for .%s lanes (no #! line, and none configured)", name, e.Ext)
	if len(e.Tried) > 0 {
		if len(e.Found) > 0 {
			fmt.Fprintf(&b, "; found %s", strings.Join(e.Found, ", "))
		} else {
			fmt.Fprintf(&b, "; none of %s is on PATH", strings.Join(e.Tried, ", "))
		}
	}
	// The portable spelling (a command name) for #! and swim.yml; the
	// found path for this machine's config.
	w := strings.Fields(ex)
	portable := strings.Join(append([]string{filepath.Base(w[0])}, w[1:]...), " ")
	fmt.Fprintf(&b, "\nFix it one way:\n  - a first line: #!/usr/bin/env %s\n  - this repo, your machine: swim config runners.%s %q\n  - everyone (CI too): add to swim.yml\n      runners:\n        %s: %s\n  - or run it once at a terminal: swim asks, and remembers the answer",
		portable, e.Ext, ex, e.Ext, portable)
	return b.String()
}

// Resolve works out how script runs, without asking anyone. A script swim
// has no runner for returns *NeedRunner (see Confirm).
func Resolve(cfg *config.Config, script string) (Plan, error) {
	data, err := os.ReadFile(script)
	if err != nil {
		return Plan{}, err
	}
	st, err := os.Stat(script)
	if err != nil {
		return Plan{}, err
	}
	executable := st.Mode()&0o111 != 0
	p := Plan{Script: script}
	if lane.IsBinary(data) {
		if !executable {
			return Plan{}, fmt.Errorf("%s is a compiled program but not executable: chmod +x %s", filepath.Base(script), filepath.Base(script))
		}
		p.Argv, p.Source, p.Wrapped = []string{script}, Direct, true
		return p, nil
	}
	p.Wrapped = !lane.UsesLib(string(data))
	ext := extOf(script)
	if ext == "sh" || ext == "bash" {
		// lane.N.sh runs with bash, as it always has (whatever its #! says).
		p.Argv, p.Source = []string{"bash", script}, Builtin
		return p, nil
	}
	if words := shebang(data); len(words) > 0 {
		p.Argv, p.Source = append(words, script), Shebang
		return p, nil
	}
	if r := cfg.Runners[ext]; r != "" && ext != "" {
		p.Argv, p.Source = append(strings.Fields(r), script), Config
		if r == "./" {
			p.Argv = []string{script}
		}
		if filepath.Base(cfg.RunnerFrom[ext]) == config.RepoFileName {
			p.Source = RepoYML
		}
		return p, nil
	}
	if executable && Candidates[ext] == nil {
		// lane.3, lane.3.bin: the operator made it executable; run it.
		p.Argv, p.Source = []string{script}, Direct
		return p, nil
	}
	if ext == "" {
		return Plan{}, fmt.Errorf("%s has no #! line, no extension and isn't executable: add a #! line, or chmod +x it", filepath.Base(script))
	}
	need := &NeedRunner{Script: script, Ext: ext}
	for _, c := range Candidates[ext] {
		w := strings.Fields(c)
		need.Tried = append(need.Tried, w[0])
		if path, err := exec.LookPath(w[0]); err == nil {
			need.Found = append(need.Found, strings.Join(append([]string{path}, w[1:]...), " "))
		}
	}
	if executable {
		need.Found = append(need.Found, "./")
	}
	return Plan{}, need
}

// shebang returns the words of data's #! line, if it has one.
func shebang(data []byte) []string {
	if len(data) < 2 || data[0] != '#' || data[1] != '!' {
		return nil
	}
	line := string(data[2:])
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return strings.Fields(strings.TrimSuffix(line, "\r"))
}

func extOf(path string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(filepath.Base(path)), "."))
}

// ErrDeclined is Confirm's error when the operator says no.
var ErrDeclined = errors.New("no runner chosen")

// Confirm asks the operator which command runs need's extension: the first
// one found on PATH (Enter or y), another one found (its number), or a
// command they type. It returns the command (an absolute path for a found
// one, "./" to run the script itself).
func Confirm(in io.Reader, out io.Writer, need *NeedRunner) (string, error) {
	name := filepath.Base(need.Script)
	fmt.Fprintf(out, "swim: %s is the first .%s lane here: it has no #! line, and no runner is set for .%s files.\n", name, need.Ext, need.Ext)
	rd := bufio.NewReader(in)
	for {
		if len(need.Found) > 0 {
			for i, f := range need.Found {
				fmt.Fprintf(out, "  %d) %s\n", i+1, describe(f))
			}
			fmt.Fprintf(out, "Run .%s lanes with %s? [Y/n, a number, or type a command]: ", need.Ext, need.Found[0])
		} else {
			fmt.Fprintf(out, "None of %s is on PATH. Command to run .%s lanes (Enter to cancel): ", strings.Join(need.Tried, ", "), need.Ext)
		}
		line, err := rd.ReadString('\n')
		ans := strings.TrimSpace(line)
		if err != nil && ans == "" {
			fmt.Fprintln(out)
			return "", ErrDeclined
		}
		switch low := strings.ToLower(ans); {
		case (low == "" || low == "y" || low == "yes") && len(need.Found) > 0:
			return need.Found[0], nil
		case low == "" || low == "n" || low == "no":
			return "", ErrDeclined
		}
		if k, err := strconv.Atoi(ans); err == nil {
			if k >= 1 && k <= len(need.Found) {
				return need.Found[k-1], nil
			}
			fmt.Fprintf(out, "swim: pick 1..%d\n", len(need.Found))
			continue
		}
		if ans == "./" {
			return ans, nil
		}
		w := strings.Fields(ans)
		path, err := exec.LookPath(w[0])
		if err != nil {
			fmt.Fprintf(out, "swim: %s: not found on PATH\n", w[0])
			continue
		}
		return strings.Join(append([]string{path}, w[1:]...), " "), nil
	}
}

func describe(cmd string) string {
	if cmd == "./" {
		return "./  (run the script itself; it is executable)"
	}
	return cmd
}

// Save records cmd as the runner for ext in root's section of the
// operator's config.
func Save(root, ext, cmd string) error {
	return config.SetKey(config.Path(), root, "runners."+ext, cmd, true)
}

// Apply builds the plan for script once its runner (from Confirm) is known,
// without re-reading config.
func Apply(script, cmd string) (Plan, error) {
	data, err := os.ReadFile(script)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Script: script, Source: Config, Wrapped: !lane.UsesLib(string(data))}
	if cmd == "./" {
		p.Argv, p.Source = []string{script}, Direct
		return p, nil
	}
	p.Argv = append(strings.Fields(cmd), script)
	return p, nil
}
