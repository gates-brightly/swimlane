// Package cli implements swim's commands.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/help"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/migrate"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
	"github.com/gates-brightly/swimlane/internal/version"
)

type command struct {
	run    func(args []string) error
	hidden bool
}

var commands map[string]command

func init() {
	commands = map[string]command{
		"init":      {run: cmdInit},
		"config":    {run: cmdConfig},
		"env":       {run: cmdEnv},
		"new":       {run: cmdNew},
		"run":       {run: cmdRun},
		"all":       {run: cmdAll},
		"plan":      {run: cmdPlan},
		"status":    {run: cmdStatus},
		"report":    {run: cmdReport},
		"step":      {run: cmdStep},
		"lib":       {run: cmdLib},
		"archive":   {run: cmdArchive},
		"clean":     {run: cmdClean},
		"pause":     {run: cmdPause},
		"unpause":   {run: cmdUnpause},
		"resume":    {run: cmdUnpause},
		"claim":     {run: cmdClaim},
		"stub":      {run: cmdStub},
		"note":      {run: cmdNote},
		"log":       {run: cmdLog},
		"lock":      {run: cmdLock},
		"changelog": {run: cmdChangelog},
		"skill":     {run: cmdSkill},
		"version":   {run: cmdVersion},
		"lint":      {run: cmdLint},
		"interrupt": {run: cmdInterrupt},
		"ci":        {run: cmdCi},
		"queue":     {run: cmdQueue},
		"secret":    {run: cmdSecret},
		"cache":     {run: cmdCache},
		"watch":     {run: cmdWatch},
		"ps":        {run: cmdPs},
		"hub":       {run: cmdHub},
		"auth":      {run: cmdAuth},
		"wait":      {run: cmdWait},
		"approve":   {run: cmdApprove},
		"revoke":    {run: cmdRevoke},
		"verify":    {run: cmdVerify},
		"history":   {run: cmdHistory},
		"chain":     {run: cmdChain},
		"keys":      {run: cmdKeys},
		"doctor":    {run: cmdDoctor},
		"help":      {run: cmdHelp},
		"guide":     {run: cmdGuide},
		// In-lane commands, for lane scripts in any language (lanecmds.go);
		// swim run "<label>" -- cmd is the in-lane form of run.
		"gate":     {run: laneGate},
		"snapshot": {run: laneSnapshot},
		"evidence": {run: laneEvidence},
		"guard":    {run: cmdGuard},
		"stage":    {run: cmdStagePublic},
		"drift":    {run: cmdDrift},
		"stop":     {run: cmdStop},
		"msg":      {run: cmdMsg},
		"input":    {run: cmdInput},
		"output":   {run: cmdOutput},
		// Called by the lane script library, not by people.
		"_start":     {run: cmdStart, hidden: true},
		"_finish":    {run: cmdFinish, hidden: true},
		"_batch":     {run: cmdBatch, hidden: true},
		"_mark":      {run: cmdMark, hidden: true},
		"_approve":   {run: cmdApproveHook, hidden: true},
		"_stage":     {run: cmdStage, hidden: true},
		"_wrap":      {run: cmdWrap, hidden: true},
		"_cache-env": {run: cmdCacheEnv, hidden: true},
	}
}

// exitError carries a specific exit code out of a command.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Main runs swim with args (without the program name) and returns the exit code.
func Main(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(helpText())
		return 0
	}
	if args[0] == "--version" { // offline and one line, for scripts; swim version also checks the latest release
		fmt.Println("swim", version.Long())
		return 0
	}
	name := args[0]
	if _, ok := commands[name]; !ok && (looksLikeLaneRef(name) || isTaskHere(name)) {
		// `swim 1 2` / `swim 3f2a9c1e` is shorthand for `swim run ...`.
		name, args = "run", append([]string{"run"}, args...)
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "swim: unknown command %q\nRun `swim --help` for the guide.\n", name)
		return 2
	}
	rest := args[1:]
	if !cmd.hidden && name != "step" && wantsHelp(rest) {
		h, _ := help.For(name)
		fmt.Print(h)
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
			h, _ := help.For(name)
			fmt.Fprint(os.Stderr, "\n"+h)
			return 2
		}
		return 1
	}
	return 0
}

// looksLikeLaneRef reports whether a first argument that isn't a command
// should be read as a lane number or job id.
func looksLikeLaneRef(s string) bool {
	if n, err := strconv.Atoi(s); err == nil {
		return n > 0
	}
	return lane.ValidJobID(s)
}

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
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return "", nil, err
	}
	// Refuse lane scripts and logs this swim can't read.
	if err := migrate.Check(root); err != nil {
		return "", nil, err
	}
	return root, cfg, nil
}

// repoNoMigrate resolves the repo root and its configuration and touches no
// files (for commands that run inside a lane script).
func repoNoMigrate() (string, *config.Config, error) {
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

// isTaskHere reports whether s names a lane (# Task:) in the repo here:
// `swim renew-certs` is then `swim run renew-certs`.
func isTaskHere(s string) bool {
	if !lane.ValidTaskName(s) {
		return false
	}
	root, cfg, err := repo()
	if err != nil {
		return false
	}
	return len(taskLanes(root, cfg, s)) > 0
}

// taskLanes are the lanes whose # Task: is name (one, unless swim lint
// has something to say).
func taskLanes(root string, cfg *config.Config, name string) []int {
	var out []int
	for n := 1; n <= cfg.Lanes; n++ {
		if info, err := lane.ReadScript(root, n); err == nil && info.Task == name {
			out = append(out, n)
		}
	}
	return out
}

// laneRef resolves a lane number, a task name (# Task:) or a pinned job id
// to a lane, in that order. A job id
// (exact, or a prefix of 8+ characters) matches the job in a lane's script on
// disk; with lastRun it also matches the job a lane last ran. Pinning by job
// means the command acts on exactly that job, or refuses if no lane holds it
// any more (the script was rewritten or stubbed).
func laneRef(root string, cfg *config.Config, s string, lastRun bool) (int, error) {
	if _, err := strconv.Atoi(s); err == nil {
		return laneArg(cfg, s)
	}
	if lane.ValidTaskName(s) {
		switch ns := taskLanes(root, cfg, s); len(ns) {
		case 1:
			return ns[0], nil
		case 0:
		default:
			return 0, fmt.Errorf("task %s is ambiguous: lanes %v both say # Task: %s (swim lint)", s, ns, s)
		}
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
			return 0, usagef("%q is neither a lane number, a task (# Task:) nor a job id (job ids need 8+ characters)%s", s, taskHint(root, cfg))
		}
		if lane.ValidTaskName(s) {
			return 0, fmt.Errorf("no lane is task %s, and no lane holds job %s%s", s, s, taskHint(root, cfg))
		}
		msg := fmt.Sprintf("no lane holds job %s", s)
		if len(held) > 0 {
			msg += " (current jobs: " + strings.Join(held, ", ") + ")"
		}
		return 0, errors.New(msg)
	}
	return 0, fmt.Errorf("job %s is ambiguous: matches lanes %v; use more characters", s, hits)
}

// taskRef is ref with a task name (# Task:) replaced by its lane's
// number, for commands that tell a lane number from a job id by its form;
// anything else comes back as it was.
func taskRef(root string, cfg *config.Config, ref string) string {
	if lane.ValidTaskName(ref) {
		if ns := taskLanes(root, cfg, ref); len(ns) == 1 {
			return strconv.Itoa(ns[0])
		}
	}
	return ref
}

// taskHint lists the repo's tasks for an error: " (tasks: a, b)", or "".
func taskHint(root string, cfg *config.Config) string {
	var names []string
	for n := 1; n <= cfg.Lanes; n++ {
		if info, err := lane.ReadScript(root, n); err == nil && info.Task != "" {
			names = append(names, info.Task)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " (tasks: " + strings.Join(names, ", ") + ")"
}

// requireVersion refuses to continue when the repo's .swim.lock pins
// another breaking version, and creates the lock if create is set and none
// exists (a swim job is about to run).
func requireVersion(root string, create bool) error {
	if !create {
		return version.Check(root)
	}
	created, err := version.Ensure(root)
	if err != nil {
		return err
	}
	if created {
		history.Log(root, history.Entry{Event: history.Lock, Detail: fmt.Sprintf("created %s: syntax %s (swim %s)", version.LockName, version.Current, version.String())})
		fmt.Fprintf(os.Stderr, "swim: wrote %s (syntax %s); commit it so everyone runs a compatible swim\n", version.LockName, version.Current)
	}
	return nil
}

// warnVersion prints a lock mismatch without stopping a read-only command.
func warnVersion(root string) {
	if err := version.Check(root); err != nil {
		fmt.Fprintf(os.Stderr, "swim: warning: %v\n\n", err)
	}
}

// laneArg parses and range-checks a lane number.
func laneArg(cfg *config.Config, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usagef("%q is not a lane number", s)
	}
	if !cfg.ValidLane(n) {
		return 0, fmt.Errorf("no swim %d: lanes are numbered 1..%d", n, lane.MaxLanes)
	}
	return n, nil
}

func self() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "swim"
}

// helpText is swim --help: the short overview on a terminal, and the full
// guide anywhere else, so an agent or a pipe still gets the manual.
func helpText() string {
	if ui.IsTTY(os.Stdout) {
		return help.Short()
	}
	return help.Full()
}

func cmdGuide(args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim guide")
	}
	fmt.Print(help.Full())
	return nil
}

func cmdHelp(args []string) error {
	if len(args) == 0 {
		fmt.Print(helpText())
		return nil
	}
	h, ok := help.For(args[0])
	if !ok {
		return usagef("no help for %q", args[0])
	}
	fmt.Print(h)
	return nil
}
