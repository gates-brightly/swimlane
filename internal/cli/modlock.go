package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/version"
)

// laneRefs are the # Modules: entries of lane n (and its jobs').
func laneRefs(root string, n int) []string {
	info, err := lane.ReadScript(root, n)
	if err != nil {
		return nil
	}
	refs := append([]string(nil), info.Modules...)
	for _, s := range info.Subs {
		refs = append(refs, s.Info.Modules...)
	}
	return refs
}

// lanesModules resolves the modules each lane in use declares: by lane,
// with each lane's resolution problem.
func lanesModules(root string, cfg *config.Config) (map[int][]*module.Module, map[int]error) {
	mods, errs := map[int][]*module.Module{}, map[int]error{}
	for _, n := range lane.Associated(root) {
		refs := laneRefs(root, n)
		if len(refs) == 0 {
			continue
		}
		ms, err := module.Resolve(root, cfg.Modules, uniq(refs))
		mods[n] = ms
		if err != nil {
			errs[n] = err
		}
	}
	return mods, errs
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// moduleLockProblem says why lane n can't run with this swim's built-in
// modules: the lock pins other versions of ones it uses ("" if none).
func moduleLockProblem(root string, cfg *config.Config, n int) string {
	lock, ok, err := version.ReadLock(root)
	if err != nil || !ok || lock.Modules == nil {
		return ""
	}
	mods, _ := module.Resolve(root, cfg.Modules, uniq(laneRefs(root, n)))
	return strings.Join(module.LockProblems(lock, mods), "; ")
}

// modulesCheck is swim modules check [--yaml]: for each module the lanes
// declare, what they ask for, what the lock pins and what this swim has,
// and what to do about a mismatch. Exit 1 on one.
func modulesCheck(root string, cfg *config.Config, args []string) error {
	var asYAML bool
	if rest, err := (flags{bools: map[string]*bool{"yaml": &asYAML}}).parse(args); err != nil {
		return err
	} else if len(rest) > 0 {
		return usagef("usage: swim modules check [--yaml]")
	}
	lock, _, err := version.ReadLock(root)
	if err != nil {
		return err
	}
	type row struct {
		Name    string `yaml:"name"`
		Source  string `yaml:"source"`
		Lanes   []int  `yaml:"lanes,flow"`
		Asked   string `yaml:"asked,omitempty"`
		Locked  string `yaml:"locked,omitempty"`
		Swim    string `yaml:"this_swim,omitempty"`
		Problem string `yaml:"problem,omitempty"`
	}
	rows := map[string]*row{}
	mods, errs := lanesModules(root, cfg)
	var lanes []int
	for n := range mods {
		lanes = append(lanes, n)
	}
	sort.Ints(lanes)
	bad := false
	for _, n := range lanes {
		refs := uniq(laneRefs(root, n))
		for _, m := range mods[n] {
			r := rows[m.Source()+" "+m.Name]
			if r == nil {
				r = &row{Name: m.Name, Source: m.Source()}
				if m.Builtin {
					r.Swim = m.Version
					if p, ok := lock.Builtin(m.Name); ok {
						r.Locked = p.Version
					} else {
						r.Locked = "-"
						r.Problem = "not pinned: swim modules lock pins it"
					}
					if why := module.LockProblem(lock, m); why != "" {
						r.Problem, bad = why, true
					}
				}
				rows[m.Source()+" "+m.Name] = r
			}
			r.Lanes = append(r.Lanes, n)
			for _, ref := range refs {
				if ref == m.Ref && strings.Contains(ref, "@") && !strings.Contains(r.Asked, ref) {
					r.Asked = strings.TrimPrefix(strings.Join([]string{r.Asked, ref}, " "), " ")
				}
			}
		}
	}
	var out []*row
	for _, r := range rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	var unresolved []string
	for _, n := range lanes {
		if err := errs[n]; err != nil {
			bad = true
			for _, l := range strings.Split(err.Error(), "\n") {
				unresolved = append(unresolved, fmt.Sprintf("swim %d: %s", n, l))
			}
		}
	}
	if asYAML {
		if err := yaml.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "swim.modules-check/v1", "swim": version.String(), "modules": out, "unresolved": unresolved}); err != nil {
			return err
		}
	} else {
		if len(out) == 0 && len(unresolved) == 0 {
			fmt.Println("no lane declares a module (# Modules:)")
		}
		for _, r := range out {
			fmt.Printf("%-12s %-18s lanes %-10s", r.Name, r.Source, intsText(r.Lanes))
			if r.Swim != "" {
				fmt.Printf(" lock %-8s this swim %s", r.Locked, r.Swim)
			}
			if r.Asked != "" {
				fmt.Printf("  asked %s", r.Asked)
			}
			fmt.Println()
			if r.Problem != "" {
				fmt.Println("  " + r.Problem)
			}
		}
		for _, u := range unresolved {
			fmt.Println(u)
		}
	}
	if bad {
		return exitError{1}
	}
	return nil
}

// modulesLock is swim modules lock: pin, in .swim.lock, the built-in
// modules the lanes use, as this swim has them.
func modulesLock(root string, cfg *config.Config, args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim modules lock")
	}
	changes, err := lockModules(root, cfg)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Printf("%s already pins the built-in modules the lanes use\n", version.LockName)
		return nil
	}
	for _, c := range changes {
		fmt.Printf("%s: %s\n", version.LockName, c)
	}
	fmt.Println("commit it; the lanes that use a changed module need approving again")
	return nil
}

// lockModules writes the built-in modules the lanes use into the lock and
// returns what changed. A lane whose modules don't resolve is an error:
// it'd leave its modules out.
func lockModules(root string, cfg *config.Config) ([]string, error) {
	mods, errs := lanesModules(root, cfg)
	for n, err := range errs {
		return nil, fmt.Errorf("swim %d: %w (fix it first: its modules would be left out of the lock)", n, err)
	}
	var all []*module.Module
	for _, ms := range mods {
		all = append(all, ms...)
	}
	pins := module.LockPins(all)
	lock, _, err := version.ReadLock(root)
	if err != nil {
		return nil, err
	}
	changes := module.PinChanges(lock, pins)
	if len(changes) == 0 {
		return nil, nil
	}
	if err := version.WriteLockModules(root, pins); err != nil {
		return nil, err
	}
	history.Log(root, history.Entry{Event: history.Lock, Detail: "modules: " + strings.Join(changes, ", ")})
	return changes, nil
}

func intsText(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ",")
}
