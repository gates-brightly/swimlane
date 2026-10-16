package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/module/gomod"
	"github.com/gates-brightly/swimlane/internal/outputs"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Environment a module command runs with.
const (
	moduleOutEnv     = "SWIM_MODULE_OUT"     // where mod_env's module writes KEY=VALUE lines
	moduleEnv        = "SWIM_MODULE"         // the module's name
	moduleVersionEnv = "SWIM_MODULE_VERSION" // its version
	moduleSecretsEnv = "SWIM_MODULE_SECRETS" // the secrets its module.yml declares
	moduleDirEnv     = "SWIM_MODULE_DIR"     // a repo module's directory (staged)
)

// laneModules are the modules lane n declares, as this round runs them:
// pinned by the manifest the launcher staged, or, for a lane run directly
// with bash, read from the working tree.
func laneModules(root string, cfg *config.Config, n int) ([]*module.Module, *manifest.Manifest, error) {
	m := manifest.FromEnv(n)
	if m == nil {
		info, err := lane.ReadScript(root, n)
		if err != nil {
			return nil, nil, err
		}
		mods, err := module.Resolve(root, cfg.Modules, info.Modules)
		return mods, nil, err
	}
	var out []*module.Module
	for _, p := range m.Pinned.Modules {
		if p.Source == module.BuiltinSource {
			b := module.Builtin(p.Name)
			switch {
			case b == nil:
				return nil, m, fmt.Errorf("module %s %s was built into the swim that staged this lane, not this one (%s)", p.Name, p.Version, os.Getenv("SWIM_BIN"))
			case b.SHA256 != p.SHA256:
				return nil, m, fmt.Errorf("module %s: this swim's built-in %s isn't the %s the manifest pinned (swim changed since the lane was staged)", p.Name, b.Version, p.Version)
			}
			out = append(out, b)
			continue
		}
		mod, err := module.FromStage(m.Dir, strings.TrimPrefix(p.Source, "./"), p.SHA256)
		if err != nil {
			return nil, m, fmt.Errorf("module %s: %w", p.Name, err)
		}
		out = append(out, mod)
	}
	return out, m, nil
}

// cmdModule is swim _module CMD [args...]: one module command, run as a
// lane's step runs it (mod and mod_env run it through swim step).
func cmdModule(args []string) error {
	if len(args) == 0 {
		return usagef("usage: swim _module CMD [args...]   (in a lane: mod CMD args, mod_env CMD args)")
	}
	cmd, rest := args[0], args[1:]
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(os.Getenv("SWIM_LANE"))
	if err != nil || n <= 0 {
		return errors.New("mod runs inside a lane (its round sets SWIM_LANE)")
	}
	mods, staged, err := laneModules(root, cfg, n)
	if err != nil {
		return err
	}
	m := module.ForCommand(mods, cmd)
	if m == nil {
		return undeclared(root, cfg, n, cmd)
	}
	out := os.Getenv(moduleOutEnv)
	if m.Effects.Credentials && out == "" {
		return fmt.Errorf("module %s gives the lane credentials: call it with mod_env, not mod", m.Name)
	}
	env := os.Environ()
	env = setEnv(env, moduleEnv, m.Name)
	env = setEnv(env, moduleVersionEnv, m.Version)
	env = setEnv(env, moduleSecretsEnv, strings.Join(m.Effects.Secrets, " "))
	if !m.Builtin {
		dir := filepath.Join(root, filepath.FromSlash(m.Dir))
		if staged != nil {
			dir = staged.Path(m.Dir)
		}
		env = setEnv(env, moduleDirEnv, dir)
	}
	if out != "" {
		// Written fresh by this call only.
		if err := os.WriteFile(out, nil, 0o600); err != nil {
			return err
		}
	}
	code, err := runModule(root, cfg, m, cmd, rest, env, out)
	if err != nil {
		return err
	}
	switch {
	case code != 0 && m.Gate:
		return exitError{module.GateExit}
	case code == module.GateExit:
		return exitError{1} // only a gate module stops the round this way
	case code != 0:
		return exitError{code}
	}
	if out == "" {
		return nil
	}
	keys, err := checkModuleEnv(m, out)
	if err != nil {
		os.WriteFile(out, nil, 0o600)
		return err
	}
	if len(keys) > 0 {
		fmt.Printf("%s: gave the lane %s (masked from now on)\n", m.Name, strings.Join(keys, " "))
	}
	return nil
}

// undeclared says why cmd isn't a command lane n may call.
func undeclared(root string, cfg *config.Config, n int, cmd string) error {
	all, _ := module.Available(root, cfg.Modules)
	if m := module.ForCommand(all, cmd); m != nil {
		return fmt.Errorf("module %s isn't declared in %s: add `# Modules: %s`", m.Name, lane.ScriptName(root, n), m.Name)
	}
	return fmt.Errorf("no module has a command %s (swim modules lists them)", cmd)
}

// runModule runs one module command and returns its exit code.
func runModule(root string, cfg *config.Config, m *module.Module, cmd string, args, env []string, out string) (int, error) {
	switch {
	case m.Kind == module.Bash:
		// The module's functions, then the command: its own bash process,
		// so nothing it defines reaches the lane.
		c := exec.Command("bash", "-c", string(m.Script())+"\n\"$@\"", m.Name, cmd)
		c.Args = append(c.Args, args...)
		return runCmd(c, root, env)
	case m.Builtin:
		ctx := &gomod.Ctx{Args: args, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: getenvFrom(env),
			Command: func(name string, a ...string) *exec.Cmd {
				c := exec.Command(name, a...)
				c.Dir, c.Env = root, env
				return c
			},
			// As swim output KEY VALUE: masked, kept with the round's outputs.
			Output: func(key, value string) error {
				dir := envOf(env, "SWIM_OUT")
				if dir == "" {
					return errors.New("SWIM_OUT is unset")
				}
				return outputs.SetValue(dir, key, cfg.Masker(env).String(value))
			},
			Mask: cfg.Masker(env).String}
		if out != "" {
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return 1, err
			}
			defer f.Close()
			ctx.Out = f
		}
		err := module.GoBuiltin(m.Name)(ctx)
		var u gomod.Usage
		switch {
		case err == nil:
			return 0, nil
		case errors.As(err, &u):
			fmt.Fprintln(os.Stderr, u.Msg)
			return 2, nil
		}
		fmt.Fprintln(os.Stderr, m.Name+": "+err.Error())
		return 1, nil
	}
	bin, err := buildModule(root, m, getenvFrom(env)(moduleDirEnv))
	if err != nil {
		return 1, err
	}
	return runCmd(exec.Command(bin, args...), root, env)
}

func runCmd(c *exec.Cmd, dir string, env []string) (int, error) {
	c.Dir, c.Env = dir, env
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return max(ee.ExitCode(), 1), nil
	}
	return 1, err
}

func getenvFrom(env []string) func(string) string {
	return func(k string) string { return envOf(env, k) }
}

// buildModule builds a repo Go module from src (its staged directory) into
// .swim/modules/build/<tree sha>-<go version>-<goos>-<goarch>, once per
// source and toolchain: with the operator's Go, offline (GOPROXY=off:
// vendor what it imports).
func buildModule(root string, m *module.Module, src string) (string, error) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("module %s is a Go module: building it needs a Go toolchain on PATH", m.Name)
	}
	ver, err := exec.Command(gobin, "env", "GOVERSION").Output()
	if err != nil {
		return "", fmt.Errorf("module %s: go env GOVERSION: %w", m.Name, err)
	}
	dir := filepath.Join(root, ".swim", "modules", "build")
	bin := filepath.Join(dir, fmt.Sprintf("%s-%s-%s-%s", m.SHA256[:32], strings.TrimSpace(string(ver)), runtime.GOOS, runtime.GOARCH))
	if st, err := os.Stat(bin); err == nil && st.Mode().IsRegular() {
		return bin, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", bin, os.Getpid())
	c := exec.Command(gobin, "build", "-trimpath", "-o", tmp, ".")
	c.Dir = src
	c.Env = setEnv(os.Environ(), "GOPROXY", "off", "GOFLAGS", "", "GOWORK", "off")
	var errb bytes.Buffer
	c.Stdout, c.Stderr = &errb, &errb
	fmt.Fprintf(os.Stderr, "%s: building ./%s (%s)\n", m.Name, m.Dir, m.SHA256[:12])
	if err := c.Run(); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("module %s: go build: %s", m.Name, strings.TrimSpace(errb.String()))
	}
	return bin, os.Rename(tmp, bin)
}

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnv are names no module may set for the lane: swim's own, and
// the ones that change how a shell or the loader runs everything after.
func reservedEnv(k string) bool {
	switch k {
	case "PATH", "IFS", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "PS4", "CDPATH", "GLOBIGNORE", "HOME", "STEP_LOG", "PROMPT_COMMAND":
		return true
	}
	for _, p := range []string{"SWIM_", "LD_", "DYLD_", "BASH_FUNC_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// credentialEnv is a name that carries a credential: only a module with
// credentials: true may give the lane one.
func credentialEnv(k string) bool {
	if redact.AutoName(k) || strings.HasSuffix(k, "_KEY") || strings.HasSuffix(k, "_KEY_ID") {
		return true
	}
	for _, p := range []string{"AWS_", "AZURE_", "ARM_", "GOOGLE_", "GCP_", "CLOUDSDK_"} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	u := strings.ToUpper(k)
	return strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD")
}

// checkModuleEnv reads what m wrote to its SWIM_MODULE_OUT, refuses what
// it may not set, and rewrites the file as checked KEY=VALUE lines.
func checkModuleEnv(m *module.Module, path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []string
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	vals := map[string]string{}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		switch {
		case !ok || !envKeyRE.MatchString(k):
			return nil, fmt.Errorf("module %s wrote %q to %s: want KEY=VALUE lines", m.Name, truncText(k, 40), moduleOutEnv)
		case reservedEnv(k):
			return nil, fmt.Errorf("module %s may not set %s for the lane", m.Name, k)
		case credentialEnv(k) && !m.Effects.Credentials:
			return nil, fmt.Errorf("module %s set %s, a credential, but its module.yml doesn't say credentials: true", m.Name, k)
		case strings.ContainsRune(v, 0):
			return nil, fmt.Errorf("module %s: %s holds a NUL byte", m.Name, k)
		}
		if _, dup := vals[k]; !dup {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("module %s: %s: %w", m.Name, moduleOutEnv, err)
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, vals[k])
	}
	return keys, os.WriteFile(path, []byte(b.String()), 0o600)
}

// readModuleEnv reads a SWIM_MODULE_OUT file checkModuleEnv rewrote.
func readModuleEnv(path string) ([][2]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && envKeyRE.MatchString(k) && !reservedEnv(k) {
			out = append(out, [2]string{k, v})
		}
	}
	return out, nil
}

// cmdModuleEnv is swim _module-env FILE: the variables a mod_env step
// gave the lane, as bash exports, with their names added to the ones
// every later step masks.
func cmdModuleEnv(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim _module-env FILE")
	}
	vars, err := readModuleEnv(args[0])
	if err != nil {
		return err
	}
	var names []string
	for _, kv := range vars {
		fmt.Printf("export %s=%s\n", kv[0], shellQuote(kv[1]))
		names = append(names, kv[0])
	}
	if len(names) > 0 {
		fmt.Printf("%s=\"${%s:+$%s }\"%s; export %s\n", config.EnvKeysVar, config.EnvKeysVar, config.EnvKeysVar, shellQuote(strings.Join(names, " ")), config.EnvKeysVar)
	}
	return nil
}

// swim modules [ls] [--yaml] | show NAME | new NAME [--kind bash|go]
func cmdModules(args []string) error {
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
		return modulesList(root, cfg, args)
	case "show":
		return modulesShow(root, cfg, args)
	case "new":
		return modulesNew(root, cfg, args)
	}
	return usagef("usage: swim modules [ls] [--yaml] | show NAME|./DIR | new NAME [--kind bash|go] [--dir DIR]")
}

// moduleDoc is one module in swim modules --yaml.
type moduleDoc struct {
	Name           string         `yaml:"name"`
	Version        string         `yaml:"version"`
	Kind           string         `yaml:"kind"`
	Source         string         `yaml:"source"`
	Summary        string         `yaml:"summary,omitempty"`
	Commands       []string       `yaml:"commands,flow"`
	Effects        module.Effects `yaml:"effects"`
	Gate           bool           `yaml:"gate,omitempty"`
	ReadOnlyFor    []string       `yaml:"read_only_for,omitempty,flow"`
	DestructiveFor []string       `yaml:"destructive_for,omitempty,flow"`
	Allowed        bool           `yaml:"allowed"`
	SHA256         string         `yaml:"sha256"`
}

func modulesList(root string, cfg *config.Config, args []string) error {
	var asYAML bool
	if rest, err := (flags{bools: map[string]*bool{"yaml": &asYAML}}).parse(args); err != nil {
		return err
	} else if len(rest) > 0 {
		return usagef("usage: swim modules ls [--yaml]")
	}
	mods, errs := module.Available(root, cfg.Modules)
	var docs []moduleDoc
	for _, m := range mods {
		docs = append(docs, moduleDoc{Name: m.Name, Version: m.Version, Kind: m.Kind, Source: m.Source(), Summary: m.Summary,
			Commands: m.Commands, Effects: m.Effects, Gate: m.Gate, ReadOnlyFor: m.ReadOnlyFor, DestructiveFor: m.DestructiveFor, Allowed: cfg.Modules.ModuleAllowed(m.Builtin, m.Name, m.Dir), SHA256: m.SHA256})
	}
	if asYAML {
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		return enc.Encode(struct {
			Schema  string      `yaml:"schema"`
			Modules []moduleDoc `yaml:"modules"`
		}{"swim.modules/v1", docs})
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	for _, d := range docs {
		src := "built in"
		if d.Source != module.BuiltinSource {
			src = d.Source
		}
		allowed := ""
		if !d.Allowed {
			allowed = "  " + p.Paint(ui.Yellow, "not allowed by swim.yml modules: allow:")
		}
		fmt.Printf("%-12s %-7s %-4s %-10s %s%s\n", d.Name, d.Version, d.Kind, src, d.Summary, allowed)
		m := &module.Module{Spec: module.Spec{Effects: d.Effects, Gate: d.Gate, ReadOnlyFor: d.ReadOnlyFor, DestructiveFor: d.DestructiveFor}}
		fmt.Printf("%s\n", p.Paint(ui.Dim, "             "+m.EffectsText()))
	}
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, p.Paint(ui.Red, "module "+e.Error()))
	}
	fmt.Println(p.Paint(ui.Dim, "swim modules show NAME prints one; a lane uses one with # Modules: NAME, then mod NAME args"))
	return nil
}

func modulesShow(root string, cfg *config.Config, args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim modules show NAME|./DIR")
	}
	all, _ := module.Available(root, cfg.Modules)
	var m *module.Module
	for _, a := range all {
		if a.Name == args[0] || a.Source() == args[0] {
			m = a
		}
	}
	if m == nil {
		var err error
		// A path the policy may not allow yet: show it anyway.
		if m, err = module.Lookup(root, &config.ModulesPolicy{Allow: []string{"./**", args[0]}, Dir: cfg.Modules.ModuleDir()}, args[0]); err != nil {
			return err
		}
	}
	src := "built into swim " + "(" + m.SHA256[:12] + ")"
	if !m.Builtin {
		src = m.Source() + " (" + m.SHA256[:12] + ")"
	}
	fmt.Printf("# %s %s, %s\n", m.Name, m.Version, src)
	fmt.Print(string(m.Files[module.SpecFile]))
	names := make([]string, 0, len(m.Files))
	for f := range m.Files {
		if f != module.SpecFile {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	for _, f := range names {
		if m.Builtin && m.Kind == module.Go {
			fmt.Printf("\n# %s: compiled into swim (source in internal/module/builtin/%s/)\n", f, m.Name)
			continue
		}
		fmt.Printf("\n# --- %s\n%s", f, m.Files[f])
	}
	return nil
}

func modulesNew(root string, cfg *config.Config, args []string) error {
	kind, dirFlag := module.Bash, ""
	rest, err := (flags{strs: map[string]*string{"kind": &kind, "dir": &dirFlag}}).parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || !module.ValidName(rest[0]) {
		return usagef("usage: swim modules new NAME [--kind bash|go] [--dir DIR]   (NAME: lower case letters, digits, -)")
	}
	name := rest[0]
	if kind != module.Bash && kind != module.Go {
		return usagef("--kind is bash or go")
	}
	parent := dirFlag
	if parent == "" {
		parent = cfg.Modules.ModuleDir()
	}
	if parent == "" {
		parent = "modules"
	}
	dir := filepath.Join(root, filepath.FromSlash(parent), name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s is there already", rel(root, dir))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	spec := fmt.Sprintf(`schema: swim.module/v1
name: %s
version: 0.1.0
kind: %s
summary: what %s does, in a line
commands: [%s]
usage:
  - mod %s ARGS
effects:                 # declared, shown in swim plan and approvals; swim can't enforce them
  network: []            # hosts it talks to
  secrets: []            # secret names it asks for (swim secret get NAME)
  writes: []             # paths outside $SWIM_OUT it writes
  credentials: false     # true: it gives the lane credentials (mod_env)
  read_only: false       # true: it changes nothing (safe before stage change)
`, name, kind, name, name, name)
	files := map[string]string{module.SpecFile: spec}
	if kind == module.Bash {
		files[name+".sh"] = fmt.Sprintf(`# %s: a swim module. bash 3.2: no associative arrays, no mapfile.
# It runs in its own bash, as a step of the lane that calls it (mod %s).

# %s ARGS - what it does.
%s() {
  if [ $# -lt 1 ]; then
    echo "%s: usage: mod %s ARGS" >&2
    return 2
  fi
  echo "%s: $*"
}
`, name, name, name, name, name, name, name)
	} else {
		files["go.mod"] = "module " + name + "\n\ngo 1.22\n"
		files["main.go"] = fmt.Sprintf(`// Command %s is a swim module: swim builds it and runs it as a step of
// the lane that calls it (mod %s ARGS). Under mod_env it may write
// KEY=VALUE lines to $SWIM_MODULE_OUT for the lane's later steps.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mod %s ARGS")
		os.Exit(2)
	}
	fmt.Println("%s:", os.Args[1:])
}
`, name, name, name, name)
	}
	for f, body := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			return err
		}
	}
	reldir := filepath.ToSlash(rel(root, dir))
	fmt.Printf("created %s/ (%s module)\n", reldir, kind)
	if !cfg.Modules.ModuleAllowed(false, name, reldir) {
		fmt.Printf("allow it in swim.yml:\n  modules:\n    allow: [./%s/*]\n    dir: %s\n", parent, parent)
	}
	fmt.Printf("use it in a lane: # Modules: ./%s, then mod %s ARGS\n", reldir, name)
	return nil
}

func truncText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
