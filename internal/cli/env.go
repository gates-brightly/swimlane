package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/envfile"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/ui"
	"gopkg.in/yaml.v3"
)

// envSchema identifies swim env --yaml output.
const envSchema = "swim.env/v1"

// swim env [N|JOB] [--yaml]: the environment env files give a lane, masked
// as the lane's logs would show it, with where each key comes from.
func cmdEnv(args []string) error {
	var asYAML bool
	rest, err := flags{bools: map[string]*bool{"yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("usage: swim env [N|JOB] [--yaml]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	n := 0
	if len(rest) == 1 {
		if n, err = laneRef(root, cfg, rest[0], false); err != nil {
			return err
		}
	}
	r, err := envfile.Resolve(root, cfg, n)
	if err != nil {
		return err
	}
	var conflicts []envfile.GuardConflict
	if n > 0 {
		info, _ := lane.ReadScript(root, n)
		lk, _ := lookup.Load(root)
		conflicts = envfile.GuardConflicts(r, info, lk)
	}
	// Masked as in the lane's logs: every file value, unless the shell
	// overrides it (then the lane sees the shell's).
	var keys []string
	for _, v := range r.Vars {
		keys = append(keys, v.Key)
	}
	mk := cfg.Masker(append(r.Env(), config.EnvKeysVar+"="+strings.Join(keys, " ")))
	where := func(v envfile.Var) string {
		if v.Line > 0 {
			return fmt.Sprintf("%s:%d", v.File, v.Line)
		}
		return v.File
	}

	if asYAML {
		type yvar struct {
			Key        string `yaml:"key"`
			Value      string `yaml:"value"`
			Scope      string `yaml:"scope"`
			From       string `yaml:"from"`
			Overridden bool   `yaml:"shell_overrides,omitempty"`
		}
		doc := struct {
			Schema    string   `yaml:"schema"`
			Lane      int      `yaml:"lane,omitempty"`
			Sources   []string `yaml:"sources"`
			SHA256    string   `yaml:"env_sha256,omitempty"`
			Vars      []yvar   `yaml:"vars"`
			Conflicts []string `yaml:"guard_conflicts,omitempty"`
		}{Schema: envSchema, Lane: n, Sources: r.Sources, SHA256: r.SHA256(), Vars: []yvar{}}
		if doc.Sources == nil {
			doc.Sources = []string{}
		}
		for _, v := range r.Vars {
			_, set := os.LookupEnv(v.Key)
			doc.Vars = append(doc.Vars, yvar{v.Key, mk.String(v.Value), v.Scope, where(v), set})
		}
		for _, c := range conflicts {
			doc.Conflicts = append(doc.Conflicts, c.Error())
		}
		out, _ := yaml.Marshal(doc)
		fmt.Print(string(out))
	} else {
		p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
		head := "swim env (general and repo; name a lane for its [lane N] keys)"
		if n > 0 {
			head = fmt.Sprintf("swim env %d", n)
		}
		if len(r.Vars) == 0 {
			fmt.Printf("%s: no env files apply (%s, .env.swim, swim.yml env:)\n", head, envfile.UserPath())
		} else {
			fmt.Println(p.Paint(ui.Bold, head) + p.Paint(ui.Dim, "  from "+strings.Join(r.Sources, ", ")))
			w := 0
			for _, v := range r.Vars {
				w = max(w, len(v.Key))
			}
			for _, v := range r.Vars {
				note := ""
				if _, set := os.LookupEnv(v.Key); set {
					note = p.Paint(ui.Yellow, "  (the shell's value wins)")
				}
				fmt.Printf("  %-*s  %-20s %-9s %s%s\n", w, v.Key, mk.String(v.Value), v.Scope, p.Paint(ui.Dim, where(v)), note)
			}
		}
		for _, c := range conflicts {
			fmt.Fprintln(os.Stderr, p.Paint(ui.Red, "error: "+c.Error()))
		}
	}
	if len(conflicts) > 0 {
		return exitError{1}
	}
	return nil
}
