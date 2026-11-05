package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/helpers"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/version"
)

// helpersDir is where swim helpers add writes modules, under the repo.
const helpersDir = "lib/swim"

// cmdHelpers is swim helpers [add MODULE... [--dir DIR] [--force]].
func cmdHelpers(args []string) error {
	var dir string
	var force bool
	rest, err := flags{bools: map[string]*bool{"force": &force}, strs: map[string]*string{"dir": &dir}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	switch {
	case len(rest) == 0:
		if force || dir != "" {
			return usagef("--dir and --force are for swim helpers add")
		}
		return listHelpers(root, cfg)
	case rest[0] == "add" && len(rest) > 1:
		if dir == "" {
			dir = helpersDir
		}
		return addHelpers(root, dir, rest[1:], force)
	}
	return usagef("usage: swim helpers [add MODULE... [--dir DIR] [--force]]")
}

// addHelpers writes each module to DIR/<module>.sh with its stamp. A copy
// already there is replaced when swim wrote it as it is (an update); a
// file swim didn't write, or a copy edited since, only with force.
func addHelpers(root, dir string, modules []string, force bool) error {
	for _, m := range modules {
		if _, ok := helpers.Source(m); !ok {
			return usagef("no helper module %q; modules: %s", m, strings.Join(helpers.Modules(), ", "))
		}
	}
	if filepath.IsAbs(dir) || strings.HasPrefix(filepath.Clean(dir), "..") {
		return usagef("--dir is a directory in the repo, not %s", dir)
	}
	var wrote []string
	for _, m := range modules {
		rel := filepath.ToSlash(filepath.Join(dir, m+".sh"))
		path := filepath.Join(root, rel)
		stamped, _ := helpers.Stamped(m, version.String())
		if old, err := os.ReadFile(path); err == nil {
			c := helpers.Check(string(old))
			switch {
			case c.State == helpers.Current:
				fmt.Printf("%s: %s is current (swim %s)\n", rel, m, c.From)
				wrote = append(wrote, rel)
				continue
			case force:
			case c.State == helpers.NotHelper:
				return fmt.Errorf("%s exists and swim didn't write it (no `# swim helpers:` stamp); move it, or --force replaces it", rel)
			case c.State == helpers.Edited:
				return fmt.Errorf("%s was edited since swim helpers add wrote it (from swim %s); --force replaces the edits with this swim's %s", rel, c.From, m)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := fsutil.WriteAtomic(path, []byte(stamped), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s (%s, from swim %s)\n", rel, m, version.String())
		wrote = append(wrote, rel)
	}
	fmt.Printf("in a lane: # Includes: %s\n", strings.Join(wrote, " "))
	for _, rel := range wrote {
		fmt.Printf("           source %s\n", rel)
	}
	return nil
}

// listHelpers lists the modules this swim ships, and the copies in this
// repo (lib/swim, and what lanes include) with how each stands.
func listHelpers(root string, cfg *config.Config) error {
	fmt.Println("modules (swim helpers add MODULE...; swim guide lanes, Shared helpers):")
	for _, m := range helpers.Modules() {
		fmt.Printf("  %-8s %s\n", m, helpers.Summary(m))
	}
	copies := helperCopies(root, cfg)
	if len(copies) == 0 {
		fmt.Println("in this repo: none")
		return nil
	}
	fmt.Println("in this repo:")
	for _, rel := range copies {
		data, _ := os.ReadFile(filepath.Join(root, rel))
		fmt.Printf("  %s  %s\n", rel, helpers.Check(string(data)).Describe(version.String()))
	}
	return nil
}

// helperCopies are the stamped copies in root: in lib/swim, and the files
// active lanes include.
func helperCopies(root string, cfg *config.Config) []string {
	seen := map[string]bool{}
	add := func(rel string) {
		if seen[rel] {
			return
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err == nil && helpers.Check(string(data)).State != helpers.NotHelper {
			seen[rel] = true
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(root, helpersDir, "*.sh")); len(matches) > 0 {
		for _, p := range matches {
			add(filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator))))
		}
	}
	for _, n := range cfg.Active {
		info, err := lane.ReadScript(root, n)
		if err != nil {
			continue
		}
		for _, g := range info.Includes {
			matches, _ := filepath.Glob(filepath.Join(root, g))
			for _, p := range matches {
				add(filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator))))
			}
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
