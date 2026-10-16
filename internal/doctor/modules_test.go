package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
)

func TestModuleGo(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "modules", "stamp")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "module.yml"), []byte("schema: swim.module/v1\nname: stamp\nversion: 1.0.0\nkind: go\ncommands: [stamp]\n"), 0o644)
	os.WriteFile(filepath.Join(d, "go.mod"), []byte("module stamp\n"), 0o644)
	os.WriteFile(filepath.Join(d, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("#!/usr/bin/env bash\n# Round: r\n# Modules: ./modules/stamp\n"), 0o755)
	o := opts(root)
	o.Cfg.Active = []int{2}
	o.Cfg.Modules = &config.ModulesPolicy{Allow: []string{"./modules/*"}}
	o.PATH = t.TempDir()
	if fs := only(Run(o), "module-go"); len(fs) != 1 || fs[0].File != "modules/stamp" {
		t.Errorf("no go: %+v", fs)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"), 0o755)
	o.PATH = bin
	if fs := only(Run(o), "module-go"); len(fs) != 0 {
		t.Errorf("with go: %+v", fs)
	}
}
