// Package gomodtest runs a built-in Go module in tests: fake tools on
// PATH, captured output, recorded outputs and variables.
package gomodtest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

// Fake is a Ctx for testing a module: fake tools (shell scripts) in a
// directory first on PATH, captured output, recorded outputs and variables.
type Fake struct {
	gomod.Ctx
	Dir     string            // the fake tools' directory (also their scratch space)
	Env     map[string]string // the lane's environment
	StdoutB bytes.Buffer
	StderrB bytes.Buffer
	OutB    bytes.Buffer      // what the module gave the lane (mod_env)
	Outputs map[string]string // what it recorded with Output
}

// NewFake makes a Fake whose tools live in dir; env is the lane's
// environment. withOut gives it a SWIM_MODULE_OUT (mod_env).
func NewFake(dir string, env map[string]string, withOut bool, args ...string) *Fake {
	f := &Fake{Dir: dir, Env: env, Outputs: map[string]string{}}
	f.Args = args
	f.Stdout, f.Stderr = &f.StdoutB, &f.StderrB
	f.Getenv = func(k string) string { return f.Env[k] }
	if withOut {
		f.Out = &f.OutB
	}
	f.Command = func(name string, a ...string) *exec.Cmd {
		c := exec.Command(name, a...)
		if p := filepath.Join(dir, name); fileExists(p) {
			c = exec.Command(p, a...)
		}
		c.Env = append(os.Environ(), "FAKE_DIR="+dir)
		for k, v := range f.Env {
			c.Env = append(c.Env, k+"="+v)
		}
		return c
	}
	f.Output = func(k, v string) error { f.Outputs[k] = v; return nil }
	f.Mask = func(s string) string { return s }
	return f
}

// Tool writes a fake tool: a bash script, with FAKE_DIR set to Dir.
func (f *Fake) Tool(name, script string) {
	os.WriteFile(filepath.Join(f.Dir, name), []byte("#!/usr/bin/env bash\n"+script), 0o755)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
