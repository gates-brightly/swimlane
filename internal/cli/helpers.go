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

// cmdHelpers is swim helpers [MODULE | add MODULE... [--dir DIR] [--force] | stats].
func cmdHelpers(args []string) error {
	var dir string
	var force bool
	rest, err := flags{bools: map[string]*bool{"force": &force}, strs: map[string]*string{"dir": &dir}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 && rest[0] == "add" {
		if len(rest) == 1 {
			return usagef("usage: swim helpers add MODULE... [--dir DIR] [--force]")
		}
		root, _, err := repo()
		if err != nil {
			return err
		}
		if dir == "" {
			dir = helpersDir
		}
		return addHelpers(root, dir, rest[1:], force)
	}
	if force || dir != "" {
		return usagef("--dir and --force are for swim helpers add")
	}
	if len(rest) > 0 && rest[0] == "stats" {
		if len(rest) > 1 {
			return usagef("usage: swim helpers stats")
		}
		root, _, err := repo()
		if err != nil {
			return err
		}
		return helperStats(root)
	}
	// Listing and showing work anywhere; in a repo they add its copies.
	root, cfg, rerr := repo()
	if rerr != nil {
		root, cfg = "", nil
	}
	switch len(rest) {
	case 0:
		return listHelpers(root, cfg)
	case 1:
		if _, ok := helpers.Source(rest[0]); !ok {
			return usagef("no helper module %q; modules: %s", rest[0], strings.Join(helpers.Modules(), ", "))
		}
		return showHelpers(root, cfg, rest[0])
	}
	return usagef("usage: swim helpers [MODULE | add MODULE... [--dir DIR] [--force] | stats]")
}

// showHelpers prints module m's functions: each one's usage and what it
// does, from the comment above it, and how a lane uses the module.
func showHelpers(root string, cfg *config.Config, m string) error {
	fmt.Printf("%s: %s\n", m, helpers.Summary(m))
	rel := filepath.ToSlash(filepath.Join(helpersDir, m+".sh"))
	if root != "" {
		installed := false
		for _, c := range helperCopies(root, cfg) {
			data, _ := os.ReadFile(filepath.Join(root, c))
			if h := helpers.Check(string(data)); h.Module == m {
				rel, installed = c, true
				fmt.Printf("in this repo: %s, %s\n", c, h.Describe(version.String()))
				break
			}
		}
		if !installed {
			fmt.Printf("not in this repo yet: swim helpers add %s\n", m)
		}
	}
	fmt.Printf("in a lane:\n  # Includes: %s\n  source %s\n", rel, rel)
	for _, f := range helpers.Funcs(m) {
		fmt.Printf("\n%s\n", f.Usage)
		for _, l := range wrapWords(f.Doc, 72) {
			fmt.Printf("    %s\n", l)
		}
	}
	return nil
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
	fmt.Println("helper modules: swim helpers MODULE shows its functions; swim helpers add MODULE... writes it into the repo")
	for _, m := range helpers.Modules() {
		fmt.Printf("  %-8s %s\n", m, helpers.Summary(m))
	}
	if root == "" {
		return nil
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

// wrapWords breaks s into lines of at most w columns, between words.
func wrapWords(s string, w int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		if line != "" && len(line)+1+len(word) > w {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// helperStep is how swim _step shows a step a swim helpers function ran
// (--helper CALL, --helper-file FILE, the file lib.sh found the function
// in): the call for its command line when FILE is this swim's module as
// shipped and defines the function, else the command in full (an edited
// or unknown copy's call says nothing a reader can trust); and the
// block's helper: line, "FUNC, FILE, how FILE stands".
func helperStep(call, file string) (shown, line string) {
	if call == "" {
		return "", ""
	}
	fn, _, _ := strings.Cut(call, " ")
	if file == "" {
		return "", fn + ", (not found), the lane's own " + fn + ": shown in full"
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fn + ", " + file + ", unreadable: shown in full"
	}
	// As the lane names it: relative to its workspace (the pinned copy),
	// where bash reports the absolute path it sourced.
	if cwd, err := os.Getwd(); err == nil && filepath.IsAbs(file) {
		if r, err := filepath.Rel(cwd, file); err == nil && !strings.HasPrefix(r, "..") {
			file = filepath.ToSlash(r)
		}
	}
	c := helpers.Check(string(data))
	src, _ := helpers.Source(c.Module)
	switch {
	case c.State == helpers.Current && strings.Contains("\n"+src, "\n"+fn+"() {"):
		return call, fmt.Sprintf("%s, %s, %s from swim %s", fn, file, c.Module, c.From)
	case c.State == helpers.Current:
		return "", fmt.Sprintf("%s, %s, %s has no %s: shown in full", fn, file, c.Module, fn)
	case c.State == helpers.Differs:
		return "", fmt.Sprintf("%s, %s, %s from swim %s, not this swim's: shown in full", fn, file, c.Module, c.From)
	case c.State == helpers.Edited:
		return "", fmt.Sprintf("%s, %s, %s edited since swim helpers add wrote it: shown in full", fn, file, c.Module)
	case c.State == helpers.Unknown:
		return "", fmt.Sprintf("%s, %s, %s, a module this swim doesn't ship: shown in full", fn, file, c.Module)
	}
	return "", fmt.Sprintf("%s, %s, not a swim helpers copy: shown in full", fn, file)
}
