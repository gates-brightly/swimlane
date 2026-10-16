package launcher

import (
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/version"
)

// PlanModule is one module a lane declares, for swim plan --yaml.
type PlanModule struct {
	Name    string         `yaml:"name"`
	Version string         `yaml:"version"`
	Source  string         `yaml:"source"` // builtin, or ./<dir>
	Effects module.Effects `yaml:"effects"`
	Gate    bool           `yaml:"gate,omitempty"` // a failure stops the round
	// The calls (by first argument) that change nothing, and those that
	// belong behind a guard.
	ReadOnlyFor    []string `yaml:"read_only_for,omitempty,flow"`
	DestructiveFor []string `yaml:"destructive_for,omitempty,flow"`
	// Lock says why .swim.lock's pin of this built-in keeps the lane from
	// running with this swim ("" if it doesn't).
	Lock string `yaml:"lock,omitempty"`
}

// planModules resolves a lane's # Modules: for swim plan: what each is and
// does, or why one doesn't resolve (the lane won't start).
func planModules(root string, cfg *config.Config, refs []string) ([]PlanModule, error) {
	mods, err := module.Resolve(root, cfg.Modules, refs)
	lock, _, _ := version.ReadLock(root)
	var out []PlanModule
	for _, m := range mods {
		out = append(out, PlanModule{Name: m.Name, Version: m.Version, Source: m.Source(), Effects: m.Effects, Gate: m.Gate,
			ReadOnlyFor: m.ReadOnlyFor, DestructiveFor: m.DestructiveFor, Lock: module.LockProblem(lock, m)})
	}
	return out, err
}

// moduleText is a module for swim plan: "notify 1.0.0 (built in: network
// the webhook's host, secret SLACK_WEBHOOK_URL)".
func moduleText(p PlanModule) string {
	src := "built in"
	if p.Source != module.BuiltinSource {
		src = p.Source
	}
	m := &module.Module{Spec: module.Spec{Effects: p.Effects, Gate: p.Gate, ReadOnlyFor: p.ReadOnlyFor, DestructiveFor: p.DestructiveFor}}
	return p.Name + " " + p.Version + " (" + src + ": " + m.EffectsText() + ")"
}
