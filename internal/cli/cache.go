package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim cache [ls] [--yaml] | rm PRESET|--all | prune [--older D] [--max SIZE] | path [PRESET] [--ci]
func cmdCache(args []string) error {
	sub := "ls"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	switch sub {
	case "ls", "list":
		return cacheList(root, cfg, args)
	case "rm":
		return cacheRemove(cfg, args)
	case "prune":
		return cachePrune(cfg, args)
	case "path":
		return cachePath(root, cfg, args)
	case "restore", "save":
		return cacheSnap(root, cfg, sub, args)
	}
	return usagef("usage: swim cache [ls] [--yaml] | rm PRESET|snap.TREE|--all | prune [--older D] [--max SIZE] | path [PRESET] [--ci] | restore|save TREE [--with S]")
}

// cacheDoc is swim cache ls --yaml.
type cacheDoc struct {
	Schema string       `yaml:"schema"`
	Repo   string       `yaml:"repo"`
	Dir    string       `yaml:"dir"`
	Size   int64        `yaml:"size_bytes"`
	Max    int64        `yaml:"max_bytes"`
	Caches []cache.Info `yaml:"caches"`
}

func cacheList(root string, cfg *config.Config, args []string) error {
	var asYAML bool
	if rest, err := (flags{bools: map[string]*bool{"yaml": &asYAML}}).parse(args); err != nil {
		return err
	} else if len(rest) > 0 {
		return usagef("usage: swim cache ls [--yaml]")
	}
	r, err := cfg.OpenCache()
	if err != nil {
		return err
	}
	list, err := r.List()
	if err != nil {
		return err
	}
	var total int64
	for _, c := range list {
		total += c.Size
	}
	if asYAML {
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(cacheDoc{Schema: "swim.cache/v1", Repo: r.Slug, Dir: r.Dir, Size: total, Max: cfg.CacheMaxBytes(), Caches: list}); err != nil {
			return err
		}
		return enc.Close()
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	limit := "no cap"
	if m := cfg.CacheMaxBytes(); m > 0 {
		limit = "of " + config.FormatSize(m)
	}
	fmt.Println(p.Paint(ui.Bold, fmt.Sprintf("swim cache · %s · %s · %s %s", r.Slug, tildePath(r.Dir), config.FormatSize(total), limit)))
	if len(list) == 0 {
		fmt.Println(p.Paint(ui.Dim, "  no caches yet: a lane declares one with # Cache: <preset> @<lockfile> ("+strings.Join(cache.Names(), ", ")+")"))
		return nil
	}
	for _, c := range list {
		used := "never used"
		if t, err := time.Parse(time.RFC3339, c.LastUsed); err == nil {
			used = "last used " + ago(time.Since(t))
		}
		by := ""
		if len(c.Lanes) > 0 {
			by = "  by " + lanesText(c.Lanes)
		}
		inUse := ""
		if c.InUse {
			inUse = p.Paint(ui.Cyan, "  in use")
		}
		fmt.Printf("  %-10s %9s   %s%s%s\n", c.Name, config.FormatSize(c.Size), used, by, inUse)
	}
	return nil
}

// ago is a duration for people: 2m ago, 3h ago, 1d ago.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
}

func tildePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func cacheRemove(cfg *config.Config, args []string) error {
	var all, yes bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "yes": &yes}}.parse(args)
	if err != nil {
		return err
	}
	if all == (len(rest) > 0) {
		return usagef("usage: swim cache rm PRESET|snap:TREE ... | --all")
	}
	r, err := cfg.OpenCache()
	if err != nil {
		return err
	}
	names := rest
	if all {
		list, err := r.List()
		if err != nil {
			return err
		}
		names = nil
		for _, c := range list {
			names = append(names, c.Name)
		}
		if len(names) == 0 {
			fmt.Println("no caches to remove")
			return nil
		}
	}
	for i, n := range names {
		if tree, ok := strings.CutPrefix(n, cache.SnapPreset+":"); ok {
			names[i] = cache.SnapName(tree) // snap:web/node_modules, as the header names it
			continue
		}
		if !cache.Known(n) {
			return fmt.Errorf("no cache %q (presets: %s; snapshots: snap.<tree>, see swim cache ls)", n, strings.Join(cache.Names(), ", "))
		}
	}
	if !yes && ui.IsTTY(os.Stdin) && ui.IsTTY(os.Stdout) {
		fmt.Printf("remove the %s cache%s of %s? the tools download again what they need [y/N] ", strings.Join(names, ", "), map[bool]string{true: "s"}[len(names) > 1], r.Slug)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Println("nothing removed")
			return nil
		}
	}
	var failed []string
	for _, n := range names {
		if err := r.Remove(n); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		fmt.Printf("removed cache %s\n", n)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}

func cachePrune(cfg *config.Config, args []string) error {
	var older, max string
	rest, err := flags{strs: map[string]*string{"older": &older, "max": &max}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim cache prune [--older D] [--max SIZE]")
	}
	var cutoff time.Time
	if older != "" {
		d, err := lookup.ParseLifetime(older)
		if err != nil || d <= 0 {
			return usagef("--older %q: a duration like 30d or 12h", older)
		}
		cutoff = time.Now().Add(-d)
	}
	limit := cfg.CacheMaxBytes()
	if max != "" {
		if limit, err = config.ParseSize(max); err != nil {
			return usagef("--max: %v", err)
		}
	}
	r, err := cfg.OpenCache()
	if err != nil {
		return err
	}
	gone, err := r.Prune(cutoff, limit)
	if err != nil {
		return err
	}
	if len(gone) == 0 {
		fmt.Printf("nothing to prune (%s in %s)\n", config.FormatSize(r.Total()), r.Slug)
		return nil
	}
	fmt.Printf("removed %s; %s left in %s\n", strings.Join(gone, ", "), config.FormatSize(r.Total()), r.Slug)
	return nil
}

func cachePath(root string, cfg *config.Config, args []string) error {
	var ci bool
	rest, err := flags{bools: map[string]*bool{"ci": &ci}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim cache path [PRESET] [--ci]")
	}
	r, err := cfg.OpenCache()
	if err != nil {
		return err
	}
	if ci {
		// What a CI cache step restores and saves around swim ci: the
		// directory, and a key per preset that changes with its lockfiles.
		return cacheCIKeys(root, cfg, r)
	}
	if len(rest) == 1 {
		p, err := r.Path(rest[0])
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	}
	fmt.Println(r.Dir)
	return nil
}

// cacheCIKeys prints dir= and one <preset>_key= line per preset the
// repo's lanes declare: lines a CI step can append to its outputs
// ($GITHUB_OUTPUT) to key actions/cache.
func cacheCIKeys(root string, cfg *config.Config, r *cache.Repo) error {
	files := map[string][]string{}
	var order []string
	for _, n := range cfg.Active {
		info, err := lane.ReadScript(root, n)
		if err != nil {
			continue
		}
		for _, d := range info.Cache {
			if d.Preset == cache.SnapPreset {
				continue // installed trees aren't a CI cache step's to share
			}
			if _, ok := files[d.Preset]; !ok {
				order = append(order, d.Preset)
			}
			files[d.Preset] = append(files[d.Preset], d.Lockfiles...)
		}
	}
	fmt.Printf("dir=%s\n", r.Dir)
	for _, p := range order {
		fmt.Printf("%s_key=swim-%s-%s-%s\n", p, p, r.Slug, cache.FilesKey(root, files[p]))
	}
	return nil
}

// cmdCacheEnv (swim _cache-env N) prints export lines for lane n's caches,
// for lane_init when a lane runs directly (bash lane.N.sh); under swim
// run the launcher sets them. It refuses as the launcher would: a missing
// lockfile, an unsafe cache directory.
func cmdCacheEnv(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim _cache-env N")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return usagef("swim _cache-env: %q isn't a lane number", args[0])
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	info, err := lane.ReadScript(root, n)
	if err != nil || len(info.Cache) == 0 {
		return err
	}
	env, err := launcher.CacheEnvFor(cfg, root, n, info)
	if err != nil {
		return fmt.Errorf("swim %d: %w", n, err)
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		fmt.Printf("export %s=%s\n", k, shellQuote(v))
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// cacheWriteLock is held by a step that writes a cache two writers can't
// share (terraform init, and Terraform's plugin cache), in a lane that
// declares it: a second lane's init waits rather than corrupting the
// cache. It returns the unlock (a no-op when no lock is needed).
func cacheWriteLock(argv []string, label string) func() {
	dir, names := os.Getenv(launcher.CacheRepoEnv), strings.Fields(os.Getenv(launcher.CachePresetsEnv))
	if dir == "" || len(names) == 0 {
		return func() {}
	}
	r := &cache.Repo{Dir: dir}
	var unlocks []func()
	for _, name := range names {
		p, ok := cache.Lookup(name)
		if !ok || !p.Exclusive || p.Writes == nil || !p.Writes(argv) {
			continue
		}
		u, err := r.Write(name, func() {
			fmt.Fprintf(os.Stderr, "swim: %q waits for the %s cache: another lane's step is writing it\n", label, name)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "swim: cache %s lock: %v (running without it)\n", name, err)
			continue
		}
		unlocks = append(unlocks, u)
	}
	return func() {
		for _, u := range unlocks {
			u()
		}
	}
}
