package lint

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// The step checks hold lint to what lib.sh's step, gate and snapshot do at
// run time (_swim_run_step): a call they would refuse, or run as something
// other than what it says, is an error here, before anything runs. The
// corpus test (internal/e2e/corpus_test.go) keeps the two in agreement.

// stepFlagOpts are a step's options that take no value (stepValueOpts,
// in checks.go, take one: --timeout 5m or --timeout=5m). Keep both in
// step with _swim_run_step.
var (
	stepFlagOpts = map[string]bool{"--soft": true, "--no-retry-timeout": true, "--expect-fail": true}
	stepOptNames = "--timeout, --retry, --backoff, --retry-on, --key, --target, --soft, --no-retry-timeout, --expect-fail"
	// stepFuncs are the library's step functions.
	stepFuncs = map[string]bool{"step": true, "gate": true, "snapshot": true}
)

// commandWords are commands a label is easily mistaken for: step echo hi
// is the label "echo" running hi.
var commandWords = map[string]bool{"echo": true, "printf": true, "ls": true, "cat": true, "bash": true, "sh": true, "true": true, "false": true,
	"test": true, "curl": true, "wget": true, "git": true, "gh": true, "kubectl": true, "helm": true, "aws": true, "gcloud": true, "az": true,
	"terraform": true, "tofu": true, "docker": true, "make": true, "go": true, "npm": true, "npx": true, "node": true, "python": true,
	"python3": true, "pip": true, "psql": true, "mysql": true, "jq": true, "grep": true, "sed": true, "awk": true, "find": true, "ssh": true,
	"scp": true, "rsync": true, "cp": true, "mv": true, "rm": true, "mkdir": true, "touch": true, "just": true, "sleep": true, "swim": true}

// childless are shell builtins that only change the shell they run in: a
// step runs its command in a process of its own, so they change nothing.
var childless = map[string]bool{"cd": true, "pushd": true, "popd": true, "export": true, "unset": true, "set": true, "source": true,
	".": true, "alias": true, "declare": true, "local": true, "readonly": true, "shift": true, "exit": true, "return": true, "read": true, "eval": true}

// checkStepCall: a step, gate or snapshot call written so lib.sh refuses it
// (an unknown option, a missing value, label or command) or runs something
// other than what it says (an unquoted label that's a command, a command
// quoted into one word, a builtin that only changes its own shell).
func checkStepCall(c *Context, s *Script) []finding.Finding {
	if !s.Lib {
		return nil
	}
	var out []finding.Finding
	for _, lc := range s.v.commands() {
		w := lc.Words
		if lc.Sub || len(w) == 0 || w[0].Quoted || !stepFuncs[w[0].Text] {
			continue
		}
		name := w[0].Text
		bad := func(fix, format string, a ...any) {
			out = append(out, f(finding.Error, "step", lc.Line, fix, format, a...))
		}
		k := 1
		ok := true
	opts:
		for ; k < len(w); k++ {
			// lib.sh sees words, not quotes: a quoted "--x" here is an
			// option to it too. A word that expands is only known then.
			t := w[k].Text
			if w[k].Expands || !strings.HasPrefix(t, "--") {
				break
			}
			opt, val, eq := strings.Cut(t, "=")
			switch {
			case t == "--":
				k++
				break opts
			case stepValueOpts[opt] && eq:
				if val == "" {
					bad("give it a value: "+opt+"=5m, or "+opt+" 5m", "%s: %s needs a value; the round would stop here", name, opt)
					ok = false
				}
			case stepValueOpts[opt]:
				if k+1 >= len(w) {
					bad("give it a value, before the label", "%s: %s needs a value; the round would stop here", name, opt)
					ok = false
				}
				k++
			case stepFlagOpts[opt] && (!eq || opt == "--expect-fail"):
			default:
				bad("the options are "+stepOptNames, "%s: unknown option %s; the round would stop here", name, t)
				ok = false
			}
		}
		if !ok {
			continue
		}
		if k >= len(w) {
			bad(fmt.Sprintf(`%s "<label>" cmd [args...]`, name), "%s with no label or command; the round would stop here", name)
			continue
		}
		label := w[k]
		k++
		if k < len(w) && w[k].Text == "--" {
			k++ // step "<label>" -- cmd: the -- ends the label
		}
		if k >= len(w) {
			bad(fmt.Sprintf(`%s "%s" cmd [args...]`, name, label.Text), "%s %q has a label but no command; the round would stop here", name, label.Text)
			continue
		}
		cmd := w[k]
		switch {
		case !label.Quoted && !label.Expands && commandWords[label.Text] && !plausibleCommand(cmd):
			// step echo hi: the label is "echo" and hi can't be a command.
			// (step test go version is a label and a command: it passes.)
			bad(fmt.Sprintf(`%s "<what it checks>" %s %s ...`, name, label.Text, cmd.Text),
				"%s %s %s ...: %q is the step's label, so it runs %q, which isn't a command here (exit 127); put a quoted label first", name, label.Text, cmd.Text, label.Text, cmd.Text)
		case cmd.Quoted && !cmd.Expands && strings.ContainsAny(cmd.Text, " \t") && !isPath(cmd.Text):
			bad(fmt.Sprintf(`%s %q %s, or bash -c '...' for a shell line`, name, label.Text, cmd.Text),
				"%s %q runs the one word %q, which no command is called (exit 127): the command and its arguments are separate words", name, label.Text, cmd.Text)
		case !cmd.Quoted && childless[cmd.Text]:
			bad("do it in the lane itself (outside the step), or inside the step's own command: bash -c 'cd dir && ...'",
				"%s %q runs %s, a shell builtin, as a program: it fails (exit 127: no such program) or, where one exists (macOS has /usr/bin/cd), changes nothing the lane sees", name, label.Text, cmd.Text)
		}
	}
	return out
}

// checkStepPipe: a pipe or a redirect after a step applies to the step
// call (swim's own output), not to the command it runs.
func checkStepPipe(c *Context, s *Script) []finding.Finding {
	if !s.Lib {
		return nil
	}
	var out []finding.Finding
	for i := 0; i < len(s.v.bare); i++ {
		if !s.v.isCode(i) || s.v.inQuote[i] {
			continue
		}
		// A command continued onto the next lines (a trailing \) is read
		// whole, as lint's commands are.
		first, bare, code := i, s.v.bare[i], s.v.code[i]
		for continued(code) && i+1 < len(s.v.code) && !s.v.doc[i+1] {
			i++
			bare = strings.TrimSuffix(strings.TrimRight(bare, " \t"), "\\") + " " + s.v.bare[i]
			code = strings.TrimSuffix(strings.TrimRight(code, " \t"), "\\") + " " + s.v.code[i]
		}
		cmds := lane.Commands(code)
		if len(cmds) == 0 || len(cmds[0]) == 0 || cmds[0][0].Quoted {
			continue
		}
		name := cmds[0][0].Text
		if !stepFuncs[name] {
			continue
		}
		if op := stepOperator(bare); op != "" {
			label := ""
			if len(cmds[0]) > 1 {
				label = cmds[0][1].Text
			}
			out = append(out, f(finding.Error, "step-pipe", first, fmt.Sprintf(`%s %q bash -c 'cmd %s ...' (in the step's own shell)`, name, label, op),
				"%s %q ... %s: the %s applies to swim's own output, not to the step's command", name, label, op, opName(op)))
		}
	}
	return out
}

// plausibleCommand reports whether a step's command word can be a command:
// one lint knows, a path, a word that expands (only known when it runs),
// or a program on PATH here.
func plausibleCommand(w lane.Word) bool {
	if w.Expands || commandWords[w.Text] || isPath(w.Text) || strings.Contains(w.Text, "/") {
		return true
	}
	_, err := exec.LookPath(w.Text)
	return err == nil
}

// isPath reports whether a command word is a path ("/opt/My Tools/bin/x"):
// quoted with its spaces, it's one command, as written.
func isPath(w string) bool {
	return strings.HasPrefix(w, "/") || strings.HasPrefix(w, "./") || strings.HasPrefix(w, "../") || strings.HasPrefix(w, "~/")
}

func opName(op string) string {
	if op == "|" {
		return "pipe"
	}
	return "redirect"
}

// stepOperator is the first pipe or redirect on a line's first command (up
// to a ;, &&, || or & at the top level, outside quotes and $(...)): "|",
// ">", ">>" or "<". A redirect to /dev/null or to a file descriptor (2>&1)
// is fine, and so is a heredoc (the step's stdin).
func stepOperator(bare string) string {
	depth := 0
	for i := 0; i < len(bare); i++ {
		c := bare[i]
		next := byte(0)
		if i+1 < len(bare) {
			next = bare[i+1]
		}
		switch {
		case (c == '$' || c == '<' || c == '>') && next == '(':
			// $(...), or a process substitution <(...) >(...): an argument
			// to the step's command, not a redirect.
			depth++
			i++
		case c == '(' && depth > 0:
			depth++
		case c == ')' && depth > 0:
			depth--
		case depth > 0:
		case c == ';' || c == '&' || c == '|' && next == '|':
			return "" // the step's command ends here; what follows is the lane's
		case c == '|':
			return "|"
		case c == '<' && next == '<':
			return ""
		case c == '>' || c == '<':
			op, j := string(c), i+1
			if c == '>' && next == '>' {
				op, j = ">>", j+1
			}
			target := strings.TrimLeft(bare[j:], " \t")
			if strings.HasPrefix(target, "/dev/null") {
				i = j
				continue
			}
			if strings.HasPrefix(target, "&") {
				i = j + strings.Index(bare[j:], "&") // past the &, so it isn't read as one
				continue
			}
			return op
		}
	}
	return ""
}

// checkShortFormShebang: a .sh lane that calls swim's steps (stage, step,
// gate, ...) but neither loads the library nor starts #!/usr/bin/env swim:
// bash runs it without them ("stage: command not found").
func checkShortFormShebang(c *Context, s *Script) []finding.Finding {
	if s.Lib || !strings.HasSuffix(s.Name, ".sh") {
		return nil
	}
	for _, lc := range s.v.commands() {
		if lc.Sub || len(lc.Words) == 0 || lc.Words[0].Quoted || !stepFuncs[lc.Words[0].Text] && lc.Words[0].Text != "stage" && lc.Words[0].Text != "guard" {
			continue
		}
		first, _, _ := strings.Cut(s.Src, "\n")
		return []finding.Finding{f(finding.Error, "short-form", 0, "make the first line #!/usr/bin/env swim",
			"%s calls swim's %s, which only a lane starting #!/usr/bin/env swim (or loading the library) has; with %q bash runs it without them (%s: command not found)",
			s.Name, lc.Words[0].Text, first, lc.Words[0].Text)}
	}
	return nil
}

// checkGuardText: what a guard does is its # Guards: line's, which the
// approval prompt and swim status show. The header must say it, and a
// guard call that says it too (guard FLAG "<text>", the older form)
// repeats it, or says something else.
func checkGuardText(c *Context, s *Script) []finding.Finding {
	if !s.Lib {
		return nil
	}
	header := map[string]string{}
	for _, g := range s.Info.Guards {
		header[g.Flag] = strings.Join(strings.Fields(g.Desc), " ")
	}
	headerAt := s.headerLine("Guards")
	var out []finding.Finding
	warned := map[string]bool{}
	for _, lc := range s.v.commands() {
		w := lc.Words
		if lc.Sub || len(w) < 3 || w[0].Quoted || w[0].Text != "guard" || w[1].Expands || w[2].Expands {
			continue
		}
		flag, desc := w[1].Text, strings.Join(strings.Fields(w[2].Text), " ")
		hd, listed := header[flag]
		if !listed || warned[flag] {
			continue
		}
		warned[flag] = true
		switch {
		case hd == "":
			out = append(out, f(finding.Warn, "guard-text", headerAt, fmt.Sprintf("# Guards: %s  %s", flag, desc),
				"# Guards: lists %s with no description: swim status and the operator see nothing of what it approves", flag))
		case hd != desc:
			out = append(out, f(finding.Warn, "guard-text", lc.Line, fmt.Sprintf("write `guard %s`: the prompt shows the header's words", flag),
				"guard %s says %q here but %q in # Guards:, which is what the operator is asked", flag, desc, hd))
		default:
			out = append(out, f(finding.Warn, "guard-text", lc.Line, fmt.Sprintf("write `guard %s` (swim lint --fix does)", flag),
				"guard %s repeats its # Guards: text; the header alone says what it does", flag))
		}
	}
	return out
}

// guardLine is the 0-based line of flag's # Guards: line, or -1.
func (s *Script) guardLine(flag string) int {
	for i, raw := range s.v.raw {
		if i >= 200 {
			break
		}
		if k, v, ok := lane.HeaderKey(strings.TrimSpace(raw)); ok && k == "Guards" {
			if f := strings.Fields(v); len(f) > 0 && f[0] == flag {
				return i
			}
		}
	}
	return -1
}

// unwritten: a guard's text missing, or still a <placeholder>.
var unwritten = lane.Unwritten

// checkGuardWhy: every declared guard says why its change is made, on
// the "#   why:" line under its # Guards: line; the operator reads it
// with what the guard does before answering. A why: line anywhere else in
// the header belongs to no guard: swim reads nothing from it.
func checkGuardWhy(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	under := false // the line before was a # Guards: line
	for i, raw := range s.v.raw {
		line := strings.TrimSpace(raw)
		if i >= 200 || line != "" && !strings.HasPrefix(line, "#") {
			break // the header is the comments at the top
		}
		if _, ok := lane.GuardWhy(line); ok {
			if !under {
				out = append(out, f(finding.Warn, "guard-why", i, "move it right under its guard's # Guards: line (no line between)",
					"this why: line is under no # Guards: line, so it belongs to no guard"))
			}
			under = false
			continue
		}
		k, _, ok := lane.HeaderKey(line)
		under = ok && k == "Guards"
	}
	for _, g := range s.Info.Guards {
		at := s.guardLine(g.Flag)
		if at < 0 || !unwritten(g.Why) || unwritten(g.Desc) {
			continue
		}
		out = append(out, f(finding.Warn, "guard-why", at, "add a line under it: #   why: <the reason for the change, with its date>",
			"guard %s doesn't say why: the operator is asked to approve %q with no reason", g.Flag, g.Desc))
	}
	return out
}

// guardDoesMax is the longest a guard's "does" text should be: one line
// of the approval prompt.
const guardDoesMax = 100

// checkGuardDoes: what a guard does is one quick line, written: a
// paragraph isn't read, and a placeholder says nothing. (Whether the line
// names the action and its target is for review: a word count can't say.)
func checkGuardDoes(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for _, g := range s.Info.Guards {
		at := s.guardLine(g.Flag)
		desc := strings.Join(strings.Fields(g.Desc), " ")
		switch {
		case at < 0:
		case unwritten(desc):
			if desc != "" {
				out = append(out, f(finding.Warn, "guard-does", at, "say the action and its exact target: remove SST stack orders-prod (14 resources)",
					"guard %s's description is still a placeholder: %s", g.Flag, desc))
			}
		case len(desc) > guardDoesMax:
			out = append(out, f(finding.Warn, "guard-does", at, "keep it to the action and target; the reason goes on its #   why: line, or split the guard",
				"guard %s's description is %d characters; the prompt shows one line (%d at most)", g.Flag, len(desc), guardDoesMax))
		}
	}
	return out
}

var (
	// stepInSubstRE: a step in $(...) or backticks; stepAfterPipeRE: a
	// step a pipe feeds (it runs in a subshell).
	stepInSubstRE   = regexp.MustCompile("(?:\\$\\(|`)\\s*(?:if\\s+|!\\s*)?(step|gate|snapshot)\\b")
	stepAfterPipeRE = regexp.MustCompile(`(?:^|[^|])\|\s*(?:if\s+|!\s*)?(step|gate|snapshot)\b`)
	// pipedLoopRE: a while or until loop a pipe feeds; loopKeywordRE: the
	// words that open and close a loop body.
	pipedLoopRE   = regexp.MustCompile(`(?:^|[^|])\|\s*(?:while|until)\b`)
	loopKeywordRE = regexp.MustCompile(`\b(?:do|done)\b`)
)

// checkStepSubshell: a step in $(...), backticks or after a pipe runs in a
// subshell. The round still ends where it fails (stop ends the whole
// round from a subshell), but the step's result is lost to the lane:
// last_failed and the round's failure count don't see it, and $(...)
// captures swim's own output, not the command's.
func checkStepSubshell(c *Context, s *Script) []finding.Finding {
	if !s.Lib {
		return nil
	}
	var out []finding.Finding
	// A loop a pipe feeds (cmd | while read x; do ... done) runs in a
	// subshell, its body too: depth counts do/done from its first do.
	loop, depth := false, 0
	for i := range s.v.raw {
		if !s.v.isCode(i) || s.v.inQuote[i] {
			continue
		}
		if !loop && pipedLoopRE.MatchString(s.v.bare[i]) {
			loop, depth = true, 0
		}
		if loop {
			inBody := depth > 0
			for _, kw := range loopKeywordRE.FindAllString(s.v.bare[i], -1) {
				if kw == "do" {
					depth++
					inBody = true
				} else {
					depth--
				}
			}
			if inBody {
				for _, cmd := range lane.Commands(s.v.code[i]) {
					if len(cmd) > 0 && !cmd[0].Quoted && stepFuncs[cmd[0].Text] {
						out = append(out, f(finding.Error, "step-subshell", i,
							"feed the loop without a pipe: while read x; do ... done < <(cmd)",
							"%s inside a loop a pipe feeds runs in a subshell: its result never reaches the lane (last_failed, any_failed, the round's counts after the loop)", cmd[0].Text))
						break
					}
				}
			}
			if inBody && depth <= 0 {
				loop = false
			}
			continue
		}
		m := stepInSubstRE.FindStringSubmatch(s.v.exp[i])
		where := "$(...)"
		if m == nil {
			m = stepAfterPipeRE.FindStringSubmatch(s.v.bare[i])
			where = "a pipe"
		}
		if m == nil {
			continue
		}
		out = append(out, f(finding.Error, "step-subshell", i,
			fmt.Sprintf("call %s on its own line; for its output, have the command write a file, or use snapshot", m[1]),
			"%s inside %s runs in a subshell: its result never reaches the lane (last_failed, the round's counts), and $(...) captures swim's output, not the command's", m[1], where))
	}
	return out
}
