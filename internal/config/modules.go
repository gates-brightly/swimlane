package config

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// ModulesPolicy is swim.yml's modules: which modules lanes may declare
// (# Modules:). It is part of the policy hash: changing it stales the
// approvals bound to a manifest.
//
//	modules:
//	  allow: [aws-session, clone, notify, ./modules/*]
//	  dir: modules          # a bare name finds modules/<name>/ too
//	  override: [notify]    # a repo module may shadow these built-ins
type ModulesPolicy struct {
	// Allow are the modules lanes may use: built-in names, and repo
	// module directories as ./path globs. Without it, built-in modules
	// are allowed and nothing else.
	Allow []string `yaml:"allow,omitempty"`
	// Dir is where a bare name finds a repo module (dir/<name>/).
	Dir string `yaml:"dir,omitempty"`
	// Override are built-in names a repo module in Dir may shadow.
	Override []string `yaml:"override,omitempty"`
}

var modNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func (p *ModulesPolicy) validate() error {
	if p == nil {
		return nil
	}
	for _, a := range p.Allow {
		if strings.HasPrefix(a, "./") {
			if _, err := path.Match(a, "./x"); err != nil || strings.Contains(a, "..") {
				return fmt.Errorf("modules: allow: %q isn't a ./path glob inside the repo", a)
			}
			continue
		}
		if !modNameRE.MatchString(a) {
			return fmt.Errorf("modules: allow: %q isn't a module name or a ./path glob", a)
		}
	}
	if d := p.Dir; d != "" && (path.IsAbs(d) || d != path.Clean(d) || d == ".." || strings.HasPrefix(d, "../")) {
		return fmt.Errorf("modules: dir: %q isn't a directory inside the repo", d)
	}
	for _, o := range p.Override {
		if !modNameRE.MatchString(o) {
			return fmt.Errorf("modules: override: %q isn't a module name", o)
		}
	}
	return nil
}

// ModuleAllowed says whether the policy lets lanes use a module: a
// built-in by its name, a repo module by its directory (./modules/x).
func (p *ModulesPolicy) ModuleAllowed(builtin bool, name, dir string) bool {
	if p == nil || p.Allow == nil {
		return builtin
	}
	for _, a := range p.Allow {
		if builtin && a == name {
			return true
		}
		if !builtin && strings.HasPrefix(a, "./") {
			if ok, _ := path.Match(a, "./"+dir); ok {
				return true
			}
		}
	}
	return false
}

// ModuleDir is where a bare name finds a repo module ("" for none).
func (p *ModulesPolicy) ModuleDir() string {
	if p == nil {
		return ""
	}
	return p.Dir
}

// Overrides says whether a repo module may shadow the built-in name.
func (p *ModulesPolicy) Overrides(name string) bool {
	if p == nil {
		return false
	}
	for _, o := range p.Override {
		if o == name {
			return true
		}
	}
	return false
}
