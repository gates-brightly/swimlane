// Package claude is the built-in claude module: it writes an artifact (a
// run report, a note) with the Claude Code CLI, from a prompt the lane
// pins and material from the round, masked. It is an artifact and nothing
// more: the reply goes to a file in $SWIM_OUT that swim never reads back,
// and the step passes or fails on whether the CLI produced a reply, never
// on what it says. No Claude-based decisions anywhere in a round.
//
//	# Modules: claude
//	# Includes: prompts/run-report.md
//	# Outputs: report.md
//	mod claude --prompt-file prompts/run-report.md --log --input "$SWIM_OUT/plan.txt" --out report.md
//
// The call says which CLI runs: everything after -- is its command, with
// any arguments a wrapper needs (-- "$CODE_CONTAINER_PATH/bin/run" --agent
// claude --), and swim's own flags follow it; --bin PATH is the short form
// for a plain binary; with neither, claude on PATH. Nothing outside the
// call (no swim setting, no environment variable) chooses it, so the
// approved script says exactly what runs. The log names the program, its
// version and its hash. It runs with no tools, no MCP servers, no slash
// commands, no saved session, a fixed system prompt, and from an empty
// directory (so no CLAUDE.md or project settings reach it). The prompt and
// inputs go on its standard input. ANTHROPIC_API_KEY comes from the lane,
// else from swim's secrets (swim secret get), else the CLI's own login.
package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod claude --out NAME (--prompt-file FILE | --prompt TEXT) [--input FILE]... [--log] [--model M] [--for 10m] [--bin PATH | -- CMD [ARGS...]]"

// systemPrompt frames every call: material in, one document out.
const systemPrompt = `You write one document for the people who run and review an operational change: a report, a note or a summary, as the request asks. Use only the material given. Cite the material for every factual claim (a step's label, a file name), and label anything you infer as a hypothesis. Never invent output, numbers or results. Reply with the document alone, in Markdown.`

// Limits on what's sent: each input, and all of it together.
const (
	maxInput = 256 << 10
	maxTotal = 1 << 20
)

// Run is mod claude ...
func Run(c *gomod.Ctx) error {
	a, err := parse(c.Args)
	if err != nil {
		return err
	}
	if a.out == "" || (a.promptFile == "") == (a.prompt == "") {
		return gomod.Usage{Msg: usage}
	}
	outDir := c.Getenv("SWIM_OUT")
	if outDir == "" {
		return errors.New("claude: SWIM_OUT is unset (it runs inside a lane's round)")
	}
	clean := filepath.ToSlash(filepath.Clean(a.out))
	if filepath.IsAbs(a.out) || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, ".swim-") {
		return gomod.Usage{Msg: "claude: --out is a file inside $SWIM_OUT (relative, no ..)"}
	}
	prompt := a.prompt
	if a.promptFile != "" {
		data, err := c.ReadPinned(a.promptFile)
		if err != nil {
			return err
		}
		prompt = string(data)
	}
	if c.Getenv("SWIM_CHECK_ONLY") == "1" {
		fmt.Fprintln(c.Stdout, "claude: not sent: swim plan --check sends nothing out")
		return nil
	}
	material, notes, err := gather(c, a)
	if err != nil {
		return err
	}
	cli := a.cli
	if len(cli) == 0 {
		cli = []string{"claude"}
		if a.bin != "" {
			cli = []string{a.bin}
		}
	}
	bin, err := resolve(c, cli[0])
	if err != nil {
		return err
	}
	ver, _ := c.Capture(bin, append(cli[1:len(cli):len(cli)], "--version")...)
	fmt.Fprintf(c.Stdout, "claude: %s (%s, sha256 %s)\n", bin, strings.TrimSpace(string(ver)), fileHash(bin)[:12])
	for _, n := range notes {
		fmt.Fprintln(c.Stdout, "claude: "+n)
	}

	var in strings.Builder
	in.WriteString(c.Mask(prompt))
	if material != "" {
		in.WriteString("\n\n# Material\n\n" + material)
	}
	// Long flags only: a wrapper in front of the CLI may have short options
	// of its own (-p).
	args := []string{"--print", "--output-format", "text", "--no-session-persistence", "--tools", "",
		"--strict-mcp-config", "--disable-slash-commands", "--system-prompt", systemPrompt}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	cmd := c.Command(bin, append(cli[1:len(cli):len(cli)], args...)...)
	// An empty directory outside any repo: no CLAUDE.md (the CLI looks in
	// parent directories too), no project settings. The same one each
	// time, so a CLI or wrapper that keeps data per directory keeps one.
	dir, cleanup, err := emptyDir()
	if err != nil {
		return err
	}
	defer cleanup()
	cmd.Dir = dir
	if c.Getenv("ANTHROPIC_API_KEY") == "" {
		if key := secret(c); key != "" {
			cmd.Env = append(cmd.Env, "ANTHROPIC_API_KEY="+key)
		}
	}
	cmd.Stdin = strings.NewReader(in.String())
	var reply strings.Builder
	cmd.Stdout = &reply
	cmd.Stderr = c.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(a.budget):
		cmd.Process.Kill()
		<-done
		return fmt.Errorf("claude: no reply within %s", a.budget)
	}
	if err != nil {
		return fmt.Errorf("claude: %s exited: %v", filepath.Base(bin), err)
	}
	text := strings.TrimSpace(reply.String())
	if text == "" {
		return errors.New("claude: an empty reply; nothing written")
	}
	// Output files aren't masked by swim: this one is, before it's written.
	text = c.Mask(text) + "\n"
	path := filepath.Join(outDir, filepath.FromSlash(clean))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(c.Stdout, "claude: wrote %s (%d bytes); swim never reads it back\n", clean, len(text))
	return nil
}

type args struct {
	out, prompt, promptFile, model, bin string
	cli                                 []string // after --: the CLI's command and its own arguments
	inputs                              []string
	log                                 bool
	budget                              time.Duration
}

func parse(in []string) (args, error) {
	a := args{budget: 10 * time.Minute}
	for i := 0; i < len(in); i++ {
		name, val, has := strings.Cut(in[i], "=")
		take := func() (string, error) {
			if has {
				return val, nil
			}
			if i+1 >= len(in) {
				return "", gomod.Usage{Msg: name + " needs a value"}
			}
			i++
			return in[i], nil
		}
		var err error
		var v string
		if in[i] == "--" {
			if a.cli = in[i+1:]; len(a.cli) == 0 {
				return a, gomod.Usage{Msg: "claude: -- needs the CLI's command after it"}
			}
			if a.bin != "" {
				return a, gomod.Usage{Msg: "claude: --bin or -- CMD, not both"}
			}
			return a, nil
		}
		switch name {
		case "--out":
			a.out, err = take()
		case "--prompt":
			a.prompt, err = take()
		case "--prompt-file":
			a.promptFile, err = take()
		case "--model":
			a.model, err = take()
		case "--bin":
			a.bin, err = take()
		case "--input":
			v, err = take()
			a.inputs = append(a.inputs, v)
		case "--log":
			a.log = !has
		case "--for":
			v, err = take()
			if err == nil {
				if a.budget, err = time.ParseDuration(v); err != nil || a.budget <= 0 {
					err = gomod.Usage{Msg: "--for takes a duration like 10m"}
				}
			}
		default:
			return a, gomod.Usage{Msg: "claude: unexpected " + in[i] + "\n" + usage}
		}
		if err != nil {
			return a, err
		}
	}
	return a, nil
}

// gather reads the inputs (and the round's log so far), masked, within the
// limits; notes say what was cut or left out.
func gather(c *gomod.Ctx, a args) (string, []string, error) {
	type item struct{ name, path string }
	var items []item
	if a.log {
		if p := c.Getenv("STEP_LOG"); p != "" {
			items = append(items, item{"this lane's log", p})
		}
	}
	for _, p := range a.inputs {
		full := p
		if !filepath.IsAbs(full) {
			full = filepath.Join(c.Getenv("SWIM_ROOT"), p)
		}
		items = append(items, item{p, full})
	}
	var b strings.Builder
	var notes []string
	total := 0
	for _, it := range items {
		f, err := os.Open(it.path)
		if err != nil {
			return "", nil, fmt.Errorf("claude: input %s: %w", it.name, err)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxInput+1))
		f.Close()
		if err != nil {
			return "", nil, err
		}
		if len(data) > maxInput {
			// The end of a log is what matters most: keep it.
			data = data[len(data)-maxInput:]
			notes = append(notes, fmt.Sprintf("%s: sent its last %d KiB", it.name, maxInput>>10))
		}
		if total+len(data) > maxTotal {
			notes = append(notes, fmt.Sprintf("%s: left out (over %d KiB in all)", it.name, maxTotal>>10))
			continue
		}
		total += len(data)
		fmt.Fprintf(&b, "<file name=%q>\n%s\n</file>\n\n", it.name, c.Mask(string(data)))
	}
	if total > 0 {
		notes = append(notes, fmt.Sprintf("sending the prompt and %d input(s), %d KiB, masked", len(items), total>>10))
	}
	return b.String(), notes, nil
}

// resolve finds the CLI's program: a path as given, or a name on the
// lane's PATH.
func resolve(c *gomod.Ctx, bin string) (string, error) {
	if strings.ContainsRune(bin, '/') {
		if st, err := os.Stat(bin); err != nil || st.IsDir() {
			return "", fmt.Errorf("claude: %s isn't a program", bin)
		}
		return bin, nil
	}
	// The lane's PATH only: what its steps would run.
	for _, d := range filepath.SplitList(c.Getenv("PATH")) {
		p := filepath.Join(d, bin)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("claude: no %s on PATH: install Claude Code, or name it in the call (--bin PATH, or -- CMD ARGS...)", bin)
}

// secret is ANTHROPIC_API_KEY from swim's secrets, if the lane may have it.
func secret(c *gomod.Ctx) string {
	swim := c.Getenv("SWIM_BIN")
	if swim == "" {
		return ""
	}
	out, err := c.Capture(swim, "secret", "get", "ANTHROPIC_API_KEY")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// emptyDir is <tmp>/swim-claude-<uid>/empty: made if missing, and used
// only while it's empty and this user's own; else a fresh one, which done
// removes.
func emptyDir() (dir string, done func(), err error) {
	dir = filepath.Join(os.TempDir(), fmt.Sprintf("swim-claude-%d", os.Getuid()), "empty")
	if err := os.MkdirAll(dir, 0o700); err == nil {
		if st, err := os.Lstat(dir); err == nil && st.IsDir() && st.Mode().Perm() == 0o700 {
			if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
				return dir, func() {}, nil
			}
		}
	}
	tmp, err := os.MkdirTemp("", "swim-claude-")
	return tmp, func() { os.RemoveAll(tmp) }, err
}

func fileHash(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return strings.Repeat("?", 12)
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}
