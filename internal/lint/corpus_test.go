package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
)

// The corpus: scripts that name what lint looks for where bash never runs
// it (quoted text, heredoc bodies, comments). Lint reports nothing for
// them, with the config that turns on the checks it gates: guarded
// secrets, and an ambient credential the text names.
func TestCorpusQuietScripts(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no secrets store: a real get would be reported
	paths, _ := filepath.Glob(filepath.Join("testdata", "corpus", "*.sh"))
	if len(paths) == 0 {
		t.Fatal("no corpus scripts")
	}
	cfg := cfg4()
	cfg.Repo.Secrets = map[string]config.SecretPolicy{"TOKEN": {Guard: config.Flags{"ALLOW_X"}}, "KEY": {Guard: config.Flags{"ALLOW_X"}}}
	cfg.AmbientCredentials = []string{"prod"}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range Lint(cfg, map[int]string{1: string(data)}, 1) {
				t.Errorf("line %d: %s: %s", f.Line, f.Code, f.Message)
			}
		})
	}
}

// The quiet corpus's words run for real: lint finds each, on its line.
func TestCorpusLoudScript(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	data, err := os.ReadFile(filepath.Join("testdata", "loud.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfg4()
	cfg.Repo.Secrets = map[string]config.SecretPolicy{"TOKEN": {Guard: config.Flags{"ALLOW_X"}}}
	cfg.AmbientCredentials = []string{"prod"}
	got := map[string]bool{}
	for _, f := range Lint(cfg, map[int]string{1: string(data)}, 1) {
		got[fmt.Sprintf("%d %s", f.Line, f.Code)] = true
	}
	for i, line := range strings.Split(string(data), "\n") {
		_, codes, ok := strings.Cut(line, " # ")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		for _, code := range strings.Split(codes, ", ") {
			if !got[fmt.Sprintf("%d %s", i+1, code)] {
				t.Errorf("line %d: no %s: %s (found %v)", i+1, code, line, got)
			}
		}
	}
}
