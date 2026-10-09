// Package cli implements swim's commands.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"swim/internal/config"
	"swim/internal/help"
	"swim/internal/lane"
	"swim/internal/status"
)

type command struct {
	run    func(args []string) error
	hidden bool
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"init":    {run: cmdInit},
		"config":  {run: cmdConfig},
		"new":     {run: cmdNew},
		"run":     {run: cmdRun},
		"status":  {run: cmdStatus},
		"step":    {run: cmdStep},
		"lib":     {run: cmdLib},
		"archive": {run: cmdArchive},
		"stub":    {run: cmdStub},
		"help":    {run: cmdHelp},
		// Called by the lane script library, not by people.
		"_start":  {run: cmdStart, hidden: true},
		"_finish": {run: cmdFinish, hidden: true},
		"_mark":   {run: cmdMark, hidden: true},
	}
}

// exitError carries a specific exit code out of a command.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Main runs swim with args (without the program name) and returns the exit code.
func Main(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(help.Full())
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Println("swim", Version)
		return 0
	}
	name := args[0]
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "swim: unknown command %q\nRun `swim --help` for the guide.\n", name)
		return 2
	}
	rest := args[1:]
	if !cmd.hidden && name != "step" && wantsHelp(rest) {
		fmt.Print(help.Commands[name])
		return 0
	}
	if name == "step" && len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Print(help.Commands[name])
		return 0
	}
	if err := cmd.run(rest); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		fmt.Fprintln(os.Stderr, "swim "+name+": "+err.Error())
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprint(os.Stderr, "\n"+help.Commands[name])
			return 2
		}
		return 1
	}
	return 0
}

// Version is set at build time with -ldflags "-X swim/internal/cli.Version=...".
var Version = "dev"

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// flags pulls --name and --name=value / --name value options out of args,
// wherever they appear, until a bare "--". Unknown --options are an error.
type flags struct {
	bools map[string]*bool
	strs  map[string]*string
}

func (f flags) parse(args []string) ([]string, error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(rest, args[i+1:]...), nil
		}
		if !strings.HasPrefix(a, "--") || a == "-" {
			rest = append(rest, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if b, ok := f.bools[name]; ok {
			*b = !hasVal || val == "true" || val == "1"
			continue
		}
		if s, ok := f.strs[name]; ok {
			if !hasVal {
				if i+1 >= len(args) {
					return nil, usagef("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
			*s = val
			continue
		}
		return nil, usagef("unknown flag --%s", name)
	}
	return rest, nil
}

// repo resolves the repo root and its configuration.
func repo() (string, *config.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	root := config.RepoRoot(cwd)
	cfg, err := config.Load(root)
	if err != nil {
		return "", nil, err
	}
	return root, cfg, nil
}

// laneRef resolves a lane number or a pinned job id to a lane. A job id
// (exact, or a prefix of 8+ characters) matches the job in a lane's script on
// disk; with lastRun it also matches the job a lane last ran. Pinning by job
// means the command acts on exactly that job, or refuses if no lane holds it
// any more (the script was rewritten or stubbed).
func laneRef(root string, cfg *config.Config, s string, lastRun bool) (int, error) {
	if _, err := strconv.Atoi(s); err == nil {
		return laneArg(cfg, s)
	}
	var st *status.File
	if lastRun {
		st, _ = status.Load(root)
	}
	var hits []int
	var held []string
	for n := 1; n <= cfg.Lanes; n++ {
		info, err := lane.ReadScript(root, n)
		if err != nil {
			return 0, err
		}
		match := info.Pending() && lane.MatchJob(info.Job, s)
		if !match && st != nil {
			if l := st.Get(n); l != nil {
				match = lane.MatchJob(l.Job, s)
			}
		}
		if match {
			hits = append(hits, n)
		}
		if info.Job != "" {
			held = append(held, fmt.Sprintf("swim %d: %s", n, info.Job))
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if len(s) < 8 && !lane.ValidJobID(s) {
			return 0, usagef("%q is neither a lane number nor a job id (job ids need 8+ characters)", s)
		}
		msg := fmt.Sprintf("no lane holds job %s", s)
		if len(held) > 0 {
			msg += " (current jobs: " + strings.Join(held, ", ") + ")"
		}
		return 0, errors.New(msg)
	}
	return 0, fmt.Errorf("job %s is ambiguous: matches lanes %v; use more characters", s, hits)
}

// laneArg parses and range-checks a lane number.
func laneArg(cfg *config.Config, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usagef("%q is not a lane number", s)
	}
	if !cfg.ValidLane(n) {
		return 0, fmt.Errorf("no swim %d: lanes are numbered 1..%d (set lanes in %s)", n, cfg.Lanes, cfg.Path)
	}
	return n, nil
}

func self() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "swim"
}

func cmdHelp(args []string) error {
	if len(args) == 0 {
		fmt.Print(help.Full())
		return nil
	}
	h, ok := help.Commands[args[0]]
	if !ok {
		return usagef("no help for %q", args[0])
	}
	fmt.Print(h)
	return nil
}
