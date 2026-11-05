// Package cli implements swim's commands.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/help"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/migrate"
	"github.com/gates-brightly/swimlane/internal/status"
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
		"status":    {run: cmdStatus},
		"report":    {run: cmdReport},
		"step":      {run: cmdStep},
		"lib":       {run: cmdLib},
		"archive":   {run: cmdArchive},
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
		"watch":     {run: cmdWatch},
		"wait":      {run: cmdWait},
		"verify":    {run: cmdVerify},
		"signoff":   {run: cmdSignoff},
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
		"guard":    {run: cmdGuard},
		"stage":    {run: cmdStagePublic},
		// Called by the lane script library, not by people.
		"_start":   {run: cmdStart, hidden: true},
		"_finish":  {run: cmdFinish, hidden: true},
		"_batch":   {run: cmdBatch, hidden: true},
		"_mark":    {run: cmdMark, hidden: true},
		"_approve": {run: cmdApproveHook, hidden: true},
		"_stage":   {run: cmdStage, hidden: true},
		"_wrap":    {run: cmdWrap, hidden: true},
	}
}

// exitError carries a specific exit code out of a command.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// Main runs swim with args (without the program name) and returns the exit code.
func Main(args []string) int {
	if len(args) == 0 && inSwimRepo() {
		// Bare swim in a swim repo: what needs you (attention.go).
		if err := cmdAttention(); err != nil {
			fmt.Fprintln(os.Stderr, "swim: "+err.Error())
			return 1
		}
		return 0
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		if len(args) > 1 && args[1] == "--full" {
			fmt.Print(help.Full())
			return 0
		}
		fmt.Print(helpText())
		return 0
	}
	if args[0] == "--version" { // offline and one line, for scripts; swim version also checks the latest release
		fmt.Println("swim", version.Long())
		return 0
	}
	name := args[0]
	if n, ok := laneFileArg(name); ok {
		// ./lane.1.sh, through its #!/usr/bin/env swim line: swim run 1, in
		// the script's repo.
		name, args = "run", append([]string{"run", strconv.Itoa(n)}, args[1:]...)
	}
	if _, ok := commands[name]; !ok && (looksLikeLaneRef(name) || isTaskHere(name)) {
		// `swim 1 2` / `swim 3f2a9c1e` is shorthand for `swim run ...`.
		name, args = "run", append([]string{"run"}, args...)
	}
	cmd, ok := commands[name]
	if !ok {
		var names []string
		for n, c := range commands {
			if !c.hidden {
				names = append(names, n)
			}
		}
		fmt.Fprintf(os.Stderr, "swim: unknown command %q%s\nRun `swim --help` for the commands.\n", name, didYouMean(name, names))
		logUsage("", fmt.Sprintf("unknown command %q", name))
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
			logUsage(name, ue.msg)
			// The usage lines, not the whole manual.
			h, _ := help.For(name)
			if u, _, ok := strings.Cut(h, "\n\n"); ok {
				h = u + "\n"
			}
			fmt.Fprint(os.Stderr, "\n"+h+"Run `swim "+name+" --help` for more.\n")
			return 2
		}
		return 1
	}
	return 0
}

// logUsage records a command swim refused as typed in .swim.log, when the
// current directory is in a repo swim is set up in (never creating one
// elsewhere): which mistakes cost a turn, for the record. Only the command
// and swim's message, not the arguments, which may hold values.
func logUsage(name, msg string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	root := config.RepoRoot(cwd)
	if _, err := os.Stat(history.Path(root)); err != nil {
		return
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	detail := msg
	if name != "" {
		detail = name + ": " + msg
	}
	history.Log(root, history.Entry{Event: history.Usage, Detail: detail})
}

// looksLikeLaneRef reports whether a first argument that isn't a command
// should be read as a lane number or job id: a number, or a job a lane
// holds (so a mistyped command isn't taken for a job nobody has).
func looksLikeLaneRef(s string) bool {
	if parts := laneArgs([]string{s}); len(parts) > 1 {
		for _, p := range parts { // 2,3: a list of them
			if !looksLikeLaneRef(p) {
				return false
			}
		}
		return true
	}
	if _, ok := laneOfPathName(s); ok {
		return true // a lane script's path: swim run resolves it (or refuses another repo's)
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n > 0
	}
	if !lane.ValidJobID(s) {
		return false
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return false
	}
	_, err = laneRef(root, cfg, s, true)
	return err == nil
}

// didYouMean is " (did you mean X?)" for the option closest to word, or ""
// when none is close.
func didYouMean(word string, options []string) string {
	best, bestD := "", 3
	for _, o := range options {
		if d := editDistance(word, o); d < bestD || d == bestD && best != "" && o < best {
			best, bestD = o, d
		}
	}
	if best == "" || bestD > max(1, len(word)/3) {
		return ""
	}
	return " (did you mean " + best + "?)"
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
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
		var known []string
		for k := range f.bools {
			known = append(known, k)
		}
		for k := range f.strs {
			known = append(known, k)
		}
		if why := flagGuess(name, f); why != "" {
			return nil, usagef("unknown flag --%s: %s", name, why)
		}
		return nil, usagef("unknown flag --%s%s", name, strings.ReplaceAll(didYouMean(name, known), "mean ", "mean --"))
	}
	return rest, nil
}

// flagGuess explains a flag people reach for that swim spells, or does,
// another way ("" when it isn't one of them).
func flagGuess(name string, f flags) string {
	switch name {
	case "json":
		if _, ok := f.bools["yaml"]; ok {
			return "swim's data output is YAML: --yaml"
		}
		return "swim's data output is YAML, where a command has it (--yaml)"
	case "dry-run", "dryrun", "plan", "noop":
		return "there's nothing to switch on: a guarded step is already a dry run until someone approves it. swim lint checks a lane without running it, and swim status N shows how swim run would decide each guard"
	}
	return ""
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
// any more (the script was rewritten or archived).
// laneArgs is lane references as typed, made what swim reads: "2,3" is 2
// and 3. (A lane script's path, lane.17.sh, is read by laneRef itself.)
// Only for commands whose arguments are all lane references.
func laneArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.Contains(a, ",") && !strings.HasPrefix(a, "-") {
			for _, p := range strings.Split(a, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// lanePath reads s as a lane script's path when it looks like one (its
// file name is lane.N or lane.N.ext): its lane number, if it is this
// repo's lane N (root/lane.N.*, symlinks resolved), or an error saying
// whose it is. Only the file name would turn ../prod-repo/lane.3.sh into
// this repo's lane 3. ok is false for anything else (a number, a job id).
func lanePath(root, s string) (n string, ok bool, err error) {
	m := laneFileRE.FindStringSubmatch(filepath.Base(s))
	if m == nil {
		return s, false, nil
	}
	abs, err := filepath.Abs(s)
	if err != nil {
		return s, true, err
	}
	if root == "" || !sameDir(filepath.Dir(abs), root) {
		return s, true, usagef("%s isn't one of this repo's lanes: swim works on %s/lane.N.* here (cd to the repo it belongs to, or name the lane by its number)", s, root)
	}
	return m[1], true, nil
}

// laneOfPathName is the lane number in a lane script's file name, without
// asking whose repo it's in (lanePath does).
func laneOfPathName(s string) (string, bool) {
	if m := laneFileRE.FindStringSubmatch(filepath.Base(s)); m != nil {
		return m[1], true
	}
	return s, false
}

// sameDir reports whether a and b are one directory, symlinks resolved.
func sameDir(a, b string) bool {
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return canon(a) == canon(b)
}

func laneRef(root string, cfg *config.Config, s string, lastRun bool) (int, error) {
	if n, ok, err := lanePath(root, s); ok {
		if err != nil {
			return 0, err
		}
		s = n
	}
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

// taskRef is ref with a task name (# Task:) or a lane script's path
// replaced by its lane's number, for commands that tell a lane number
// from a job id by its form; anything else comes back as it was. A path
// to another repo's lane is an error.
func taskRef(root string, cfg *config.Config, ref string) (string, error) {
	if n, ok, err := lanePath(root, ref); ok {
		return n, err
	}
	if lane.ValidTaskName(ref) {
		if ns := taskLanes(root, cfg, ref); len(ns) == 1 {
			return strconv.Itoa(ns[0]), nil
		}
	}
	return ref, nil
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
// another breaking version. When run is set (a swim job is about to run),
// a repo with no lock isn't set up: swim init writes it, not a run.
func requireVersion(root string, run bool) error {
	if !run {
		return version.Check(root)
	}
	if _, ok, err := version.ReadLock(root); err != nil {
		return err
	} else if !ok {
		return usagef("this repo isn't set up for swim (no %s): run swim init", version.LockName)
	}
	return version.Check(root)
}

// ensureLock checks the repo's .swim.lock and writes it if it's missing:
// swim init, and swim ci, whose checkout is thrown away.
func ensureLock(root string) error {
	created, err := version.Ensure(root)
	if err != nil {
		return err
	}
	if created {
		history.Log(root, history.Entry{Event: history.Lock, Detail: fmt.Sprintf("created %s: syntax %s (swim %s)", version.LockName, version.Current, version.String())})
		fmt.Fprintf(os.Stderr, "swim: wrote %s; commit it, so everyone on this repo runs a compatible swim\n", version.LockName)
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
	if n, ok, err := lanePath(cfg.Root, s); ok {
		if err != nil {
			return 0, err
		}
		s = n
	}
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

// helpText is swim --help: the short overview, on a terminal or not, so
// a person piping it to less or grep reads what they'd see. The manual is
// swim guide (or swim --help --full).
func helpText() string {
	return help.Short()
}

func cmdGuide(args []string) error {
	switch {
	case len(args) == 0:
		fmt.Print(help.Index)
	case len(args) == 1 && args[0] == "all":
		fmt.Print(help.Full())
	case len(args) == 1:
		page, ok := help.Topic(args[0])
		if !ok {
			return usagef("no topic %q; topics: %s, all", args[0], strings.Join(help.Topics, ", "))
		}
		fmt.Print(page)
	default:
		return usagef("usage: swim guide [TOPIC|all]")
	}
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

// laneFileArg reports whether arg is a path to a lane script (lane.N.sh,
// lane.N.py, ...), as the kernel passes it when a short-form lane runs
// through its #! line, and moves to the script's directory so the run is in
// its repo.
func laneFileArg(arg string) (int, bool) {
	if !strings.ContainsRune(arg, os.PathSeparator) {
		return 0, false
	}
	m := laneFileRE.FindStringSubmatch(filepath.Base(arg))
	if m == nil {
		return 0, false
	}
	if st, err := os.Stat(arg); err != nil || st.IsDir() {
		return 0, false
	}
	if err := os.Chdir(filepath.Dir(arg)); err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

var laneFileRE = regexp.MustCompile(`^lane\.([0-9]+)(\.[A-Za-z0-9]+)?$`)

// plural is "1 file", "3 files" ("1 entry", "3 entries").
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(word, "y"))
	}
	return fmt.Sprintf("%d %ss", n, word)
}
