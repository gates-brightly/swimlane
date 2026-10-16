package launcher

import (
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/module"
)

// PlanModule is one module a lane declares, for swim plan --yaml.
type PlanModule struct {
	Name    string         `yaml:"name"`
	Version string         `yaml:"version"`
	Source  string         `yaml:"source"` // builtin, or ./<dir>
	Effects module.Effects `yaml:"effects"`
}

// planModules resolves a lane's # Modules: for swim plan: what each is and
// does, or why one doesn't resolve (the lane won't start).
func planModules(root string, cfg *config.Config, refs []string) ([]PlanModule, error) {
	mods, err := module.Resolve(root, cfg.Modules, refs)
	var out []PlanModule
	for _, m := range mods {
		out = append(out, PlanModule{Name: m.Name, Version: m.Version, Source: m.Source(), Effects: m.Effects})
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
	m := &module.Module{Spec: module.Spec{Effects: p.Effects}}
	return p.Name + " " + p.Version + " (" + src + ": " + m.EffectsText() + ")"
}
