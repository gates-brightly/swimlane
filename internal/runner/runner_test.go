package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
)

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "tsx"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin)
	write := func(name, src string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(src), mode)
		return p
	}
	cfg := &config.Config{}

	sh := write("lane.1.sh", "#!/usr/bin/env zsh\n_swim_lib=$(swim lib); eval \"$_swim_lib\"\nlane_init 1\n", 0o644)
	if p, err := Resolve(cfg, sh); err != nil || strings.Join(p.Argv, " ") != "bash "+sh || p.Wrapped || p.Source != Builtin {
		t.Errorf("sh: %+v %v", p, err)
	}
	plain := write("lane.2.sh", "echo hi\n", 0o644)
	if p, _ := Resolve(cfg, plain); !p.Wrapped {
		t.Errorf("a .sh without lane_init runs wrapped: %+v", p)
	}
	py := write("lane.3.py", "#!/usr/bin/env python3 -u\nprint(1)\n", 0o644)
	if p, err := Resolve(cfg, py); err != nil || strings.Join(p.Argv, " ") != "/usr/bin/env python3 -u "+py || p.Source != Shebang || !p.Wrapped {
		t.Errorf("shebang: %+v %v", p, err)
	}
	ts := write("lane.4.ts", "// Round: x\n", 0o644)
	_, err := Resolve(cfg, ts)
	var need *NeedRunner
	if !errors.As(err, &need) || need.Ext != "ts" || len(need.Found) != 1 || need.Found[0] != filepath.Join(bin, "tsx") {
		t.Fatalf("ts: %v", err)
	}
	for _, want := range []string{"#!/usr/bin/env tsx", "swim config runners.ts \"" + filepath.Join(bin, "tsx") + "\"", "ts: tsx"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%s", want, err)
		}
	}
	cfg.Runners, cfg.RunnerFrom = map[string]string{"ts": "tsx --quiet"}, map[string]string{"ts": "/repo/swim.yml"}
	if p, err := Resolve(cfg, ts); err != nil || strings.Join(p.Argv, " ") != "tsx --quiet "+ts || p.Source != RepoYML || p.Runner() != "tsx --quiet (swim.yml)" {
		t.Errorf("configured: %+v %v", p, err)
	}
	exe := write("lane.5", "\x7fELF\x00binary", 0o755)
	if p, err := Resolve(cfg, exe); err != nil || len(p.Argv) != 1 || p.Source != Direct || !p.Wrapped {
		t.Errorf("binary: %+v %v", p, err)
	}
	noexec := write("lane.6", "\x7fELF\x00binary", 0o644)
	if _, err := Resolve(cfg, noexec); err == nil || !strings.Contains(err.Error(), "chmod +x") {
		t.Errorf("non-executable binary: %v", err)
	}
}

func TestConfirm(t *testing.T) {
	need := &NeedRunner{Script: "/r/lane.1.ts", Ext: "ts", Found: []string{"/bin/tsx", "/bin/bun"}, Tried: []string{"tsx", "bun"}}
	var out strings.Builder
	sh, _ := exec.LookPath("sh")
	for in, want := range map[string]string{"\n": "/bin/tsx", "y\n": "/bin/tsx", "2\n": "/bin/bun", "9\n1\n": "/bin/tsx", "sh -e\n": sh + " -e"} {
		got, err := Confirm(strings.NewReader(in), &out, need)
		if err != nil || got != want {
			t.Errorf("answer %q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := Confirm(strings.NewReader("n\n"), &out, need); !errors.Is(err, ErrDeclined) {
		t.Errorf("n: %v", err)
	}
	if _, err := Confirm(strings.NewReader(""), &out, need); !errors.Is(err, ErrDeclined) {
		t.Errorf("EOF: %v", err)
	}
	none := &NeedRunner{Script: "/r/lane.1.ts", Ext: "ts", Tried: []string{"tsx"}}
	if _, err := Confirm(strings.NewReader("\n"), &out, none); !errors.Is(err, ErrDeclined) {
		t.Errorf("nothing found, Enter: %v", err)
	}
}
