package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

// fakeClaude records its arguments, directory and input, and replies.
const fakeClaude = `if [ "$1" = --version ]; then echo "9.9.9 (fake)"; exit 0; fi
printf '%s\n' "$@" >"$FAKE_DIR/args"
pwd >"$FAKE_DIR/pwd"
cat >"$FAKE_DIR/stdin"
echo "key=${ANTHROPIC_API_KEY:-none}" >"$FAKE_DIR/key"
[ -n "$EMPTY" ] && exit 0
echo "# Report"
echo "The secret is hunter2-secret."`

func TestClaude(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(root, "prompts"), 0o755)
	os.WriteFile(filepath.Join(root, "prompts", "report.md"), []byte("Write the run report."), 0o644)
	os.WriteFile(filepath.Join(out, "plan.txt"), []byte("Plan: 1 to add. password=hunter2-secret\n"), 0o644)
	log := filepath.Join(t.TempDir(), "agent1.log")
	os.WriteFile(log, []byte("PASS  tf plan\n"), 0o644)
	dir := t.TempDir()
	env := map[string]string{"SWIM_ROOT": root, "SWIM_OUT": out, "STEP_LOG": log, "SWIM_CLAUDE_BIN": filepath.Join(dir, "claude"), "ANTHROPIC_API_KEY": "sk-ant-x"}
	f := gomodtest.NewFake(dir, env, false, "--prompt-file", "prompts/report.md", "--input", filepath.Join(out, "plan.txt"), "--log", "--out", "notes/report.md", "--model", "claude-sonnet-5")
	f.Tool("claude", fakeClaude)
	f.Mask = func(s string) string { return strings.ReplaceAll(s, "hunter2-secret", "***") }
	if err := Run(&f.Ctx); err != nil {
		t.Fatalf("%v\n%s%s", err, f.StdoutB.String(), f.StderrB.String())
	}
	got, _ := os.ReadFile(filepath.Join(out, "notes", "report.md"))
	if string(got) != "# Report\nThe secret is ***.\n" {
		t.Errorf("artifact: %q", got)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, want := range []string{"-p\n", "--tools\n\n", "--strict-mcp-config", "--no-session-persistence", "--disable-slash-commands", "--model\nclaude-sonnet-5", "--system-prompt\nYou write one document"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	for _, want := range []string{"Write the run report.", "# Material", `<file name="this lane's log">`, "PASS  tf plan", "password=***"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("stdin lacks %q:\n%s", want, stdin)
		}
	}
	if strings.Contains(string(stdin), "hunter2") {
		t.Error("an unmasked secret was sent")
	}
	if pwd, _ := os.ReadFile(filepath.Join(dir, "pwd")); strings.HasPrefix(string(pwd), root) {
		t.Errorf("ran in the repo: %s", pwd)
	}
	if !strings.Contains(f.StdoutB.String(), "9.9.9 (fake), sha256 ") || !strings.Contains(f.StdoutB.String(), "wrote notes/report.md") {
		t.Errorf("log: %s", f.StdoutB.String())
	}

	// An empty reply fails; nothing is written.
	env["EMPTY"] = "1"
	f = gomodtest.NewFake(dir, env, false, "--prompt", "x", "--out", "empty.md")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "empty reply") {
		t.Errorf("empty: %v", err)
	}
	delete(env, "EMPTY")
	// --bin wins; a missing one says how to fix it.
	f = gomodtest.NewFake(dir, env, false, "--prompt", "x", "--out", "r.md", "--bin", "/nope/claude")
	if err := Run(&f.Ctx); err == nil || !strings.Contains(err.Error(), "isn't a program") {
		t.Errorf("bin: %v", err)
	}
	delete(env, "SWIM_CLAUDE_BIN")
	env["PATH"] = t.TempDir()
	f = gomodtest.NewFake(dir, env, false, "--prompt", "x", "--out", "r.md")
	if _, err := resolve(&f.Ctx, "claude-nonexistent-xyz"); err == nil || !strings.Contains(err.Error(), "SWIM_CLAUDE_BIN") {
		t.Errorf("no cli: %v", err)
	}
	for _, bad := range [][]string{{"--prompt", "x"}, {"--out", "r.md"}, {"--prompt", "x", "--prompt-file", "p", "--out", "r"}, {"--prompt", "x", "--out", "../r"}} {
		f := gomodtest.NewFake(dir, env, false, bad...)
		if err := Run(&f.Ctx); err == nil {
			t.Errorf("%v passed", bad)
		}
	}
	// A read-only check sends nothing.
	env["SWIM_CHECK_ONLY"] = "1"
	os.Remove(filepath.Join(dir, "stdin"))
	f = gomodtest.NewFake(dir, env, false, "--prompt", "x", "--out", "c.md", "--bin", filepath.Join(dir, "claude"))
	if err := Run(&f.Ctx); err != nil || !strings.Contains(f.StdoutB.String(), "not sent") {
		t.Errorf("check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stdin")); err == nil {
		t.Error("a check called the CLI")
	}
}
