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

// modulesGet is swim modules get [--upgrade [NAME|ADDRESS]]: fetch the
// modules the lanes name by source address (# Modules:
// github.com/org/mods//pagerduty@2), into .swim/modules/, and pin each in
// .swim.lock: its version (a tag), commit and files' hash. A version the
// lock already pins that the lanes accept is kept; --upgrade takes the
// newest tag they accept, for every fetched module or the one named.
func modulesGet(root string, cfg *config.Config, args []string) error {
	upgrade, only := false, ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--upgrade":
			upgrade = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				only = args[i+1]
				i++
			}
		default:
			return usagef("usage: swim modules get [--upgrade [NAME|ADDRESS]]")
		}
	}
	// The fetched modules the lanes name, one ref per address.
	refs := map[string]module.Ref{}
	var order []string
	for _, n := range lane.Associated(root) {
		for _, text := range laneRefs(root, n) {
			r, err := module.ParseRef(text)
			if err != nil || r.Address == "" {
				continue
			}
			if prev, ok := refs[r.Address]; ok && prev.Text != r.Text {
				return fmt.Errorf("lanes name %s two ways (%s and %s): name it one way", r.Address, prev.Text, r.Text)
			}
			if _, ok := refs[r.Address]; !ok {
				order = append(order, r.Address)
			}
			refs[r.Address] = r
		}
	}
	if len(refs) == 0 {
		fmt.Println("no lane names a fetched module (# Modules: host/owner/repo//dir@VERSION)")
		return nil
	}
	if only != "" {
		found := false
		for _, a := range order {
			found = found || a == only || refs[a].Name == only
		}
		if !found {
			return fmt.Errorf("--upgrade %s: no lane names that fetched module", only)
		}
	}
	sort.Strings(order)
	lock, _, err := version.ReadLock(root)
	if err != nil {
		return err
	}
	pins := map[string]version.FetchedModule{}
	if lock.Modules != nil {
		for a, p := range lock.Modules.Fetched {
			if _, used := refs[a]; used {
				pins[a] = p // one no lane names any more is dropped
			}
		}
	}
	var changes []string
	for _, a := range order {
		r := refs[a]
		up := upgrade && (only == "" || only == a || only == r.Name)
		if !cfg.Modules.FetchedAllowed(a) {
			return fmt.Errorf("%s isn't allowed: add it (or a glob matching it) to swim.yml modules: allow:", a)
		}
		f, err := module.Get(root, r, up)
		if err != nil {
			return err
		}
		pins[a] = f.Pin
		switch {
		case f.Was.SHA256 == "":
			changes = append(changes, fmt.Sprintf("%s %s (new, commit %s)", a, f.Pin.Version, short12(f.Pin.Commit)))
		case f.Was.SHA256 != f.Pin.SHA256:
			changes = append(changes, fmt.Sprintf("%s %s -> %s", a, f.Was.Version, f.Pin.Version))
		default:
			fmt.Printf("%s %s: pinned, and here\n", a, f.Pin.Version)
		}
	}
	if lock.Modules != nil {
		for a, p := range lock.Modules.Fetched {
			if _, used := refs[a]; !used {
				changes = append(changes, fmt.Sprintf("%s %s (removed: no lane names it)", a, p.Version))
			}
		}
	}
	if len(changes) == 0 {
		return nil
	}
	if err := version.WriteLockFetched(root, pins); err != nil {
		return err
	}
	sort.Strings(changes)
	for _, c := range changes {
		fmt.Printf("%s: %s\n", version.LockName, c)
	}
	history.Log(root, history.Entry{Event: history.Lock, Detail: "fetched modules: " + strings.Join(changes, ", ")})
	fmt.Println("commit it; the lanes that use a changed module need approving again")
	return nil
}
