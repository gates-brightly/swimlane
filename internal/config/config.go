// Package config loads ~/.config/swim/config.yml: global defaults plus
// per-repo sections keyed by the repo's git toplevel path.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/redact"
)

// DefaultLanes is used when neither the defaults nor the repo set `lanes`.
const DefaultLanes = 4

// Settings is one block of configuration, either `defaults` or a repo entry.
// Zero values mean "not set" so a repo block can override field by field.
type Settings struct {
	Lanes              int           `yaml:"lanes,omitempty"`
	HeaderEnv          []string      `yaml:"header_env,omitempty"`
	Runtime            string        `yaml:"runtime,omitempty"`
	Toolchain          string        `yaml:"toolchain,omitempty"`
	Deps               map[int][]int `yaml:"deps,omitempty"`
	MaxParallel        *int          `yaml:"max_parallel,omitempty"`         // lanes running at once; 0 = unlimited
	BlockedCommands    []string      `yaml:"blocked_commands,omitempty"`     // added to the built-in git push/commit/pull
	Chime              *Chime        `yaml:"chime,omitempty"`                // on | off | failure (YAML true/false/failure)
	ChimeStyle         *string       `yaml:"chime_style,omitempty"`          // bell | sound | notify
	ChimeMinS          *int          `yaml:"chime_min_s,omitempty"`          // don't chime for runs shorter than this
	SecretEnv          []string      `yaml:"secret_env,omitempty"`           // values of these variables are masked (***)
	SecretEnvAuto      *bool         `yaml:"secret_env_auto,omitempty"`      // also *_TOKEN, *_SECRET, *_PASSWORD, ... (default true)
	SecretEnvIgnore    []string      `yaml:"secret_env_ignore,omitempty"`    // names exempt from secret_env_auto
	SecretPatternsAuto *bool         `yaml:"secret_patterns_auto,omitempty"` // also AWS key ids, GitHub/Slack tokens, JWTs (default true)
	Interrupt          string        `yaml:"interrupt,omitempty"`            // graceful (default) | immediate: what the first Ctrl-C does
	Approvals          string        `yaml:"approvals,omitempty"`            // prompt (default) | flags: may an unset guard flag ask the operator
	SigningKey         string        `yaml:"signing_key,omitempty"`          // SSH key the runner signs the chain with (default ~/.ssh/id_ed25519)
	SecretsKey         string        `yaml:"secrets_key,omitempty"`          // SSH key secrets are encrypted to (default signing_key, then ~/.ssh/id_ed25519)
	Signing            string        `yaml:"signing,omitempty"`              // auto (default: sign if a key loads) | required | off
	InterruptGrace     string        `yaml:"interrupt_grace,omitempty"`      // force quit: SIGINT, then SIGKILL after this (default 5s)
	TermGrace          string        `yaml:"term_grace,omitempty"`           // SIGTERM: graceful, then force quit after this (default 10s)
	MsgLogInterval     string        `yaml:"msg_log_interval,omitempty"`     // at most one MSG log line per this per lane (default 10s; 0: every change)
	OutputsKeep        string        `yaml:"outputs_keep,omitempty"`         // round outputs older than this are pruned by swim doctor --fix (default 14d)
	// Runners maps a lane script's extension (ts, py, ...) to the command
	// that runs it ("tsx", "/usr/local/bin/python3"). A script's #! line
	// wins; swim asks the operator for one the first time it meets an
	// extension with neither, and saves the answer in the repo's section.
	Runners map[string]string `yaml:"runners,omitempty"`
	// RunnerFrom says where each runner came from (a config path or
	// swim.yml), for messages.
	RunnerFrom map[string]string `yaml:"-"`
	// Env is swim.yml's env: shared, non-secret defaults for every lane
	// (see the envfile package). Only swim.yml may set it; personal values
	// go in ~/.config/swim/env.swim.
	Env map[string]string `yaml:"env,omitempty"`
}

// File is the on-disk shape of config.yml.
type File struct {
	Defaults Settings            `yaml:"defaults"`
	Repos    map[string]Settings `yaml:"repos"`
}

// Config is the effective configuration for one repo.
type Config struct {
	Path    string // config.yml location
	Root    string // repo root the settings apply to
	HasRepo bool   // a repos entry exists for Root
	// RepoFile is the repo's committed swim.yml, if it has one ("" if not).
	// Its settings apply on top of defaults and under the repos entry, so a
	// CI runner with no config of its own gets them.
	RepoFile string
	// RepoLanes reports whether lanes was set explicitly (repos entry or
	// swim.yml), not defaulted.
	RepoLanes bool
	Settings
}

// Path returns the config file location, honouring XDG_CONFIG_HOME.
func Path() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "swim", "config.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "swim", "config.yml")
}

// RepoRoot returns the git toplevel containing dir, or dir itself outside git.
// SWIM_ROOT (exported by lane scripts) wins so steps resolve the same repo as
// the round that runs them.
func RepoRoot(dir string) string {
	if r := os.Getenv("SWIM_ROOT"); r != "" {
		return r
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil {
		if r := strings.TrimSpace(string(out)); r != "" {
			return r
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// Load reads the config file and resolves the effective settings for root.
// A missing file is not an error: built-in defaults apply.
func Load(root string) (*Config, error) {
	c := &Config{Path: Path(), Root: root}
	f, err := readFile(c.Path)
	if err != nil {
		return nil, err
	}
	if f.Defaults.Env != nil {
		return nil, fmt.Errorf("%s: env: belongs in %s (or a repo's swim.yml), not config.yml", c.Path, filepath.Join(filepath.Dir(c.Path), "env.swim"))
	}
	c.Settings = f.Defaults
	c.Runners, c.RunnerFrom = nil, nil
	c.mergeRunners(f.Defaults.Runners, c.Path)
	if s, path, err := readRepoFile(root); err != nil {
		return nil, err
	} else if path != "" {
		c.RepoFile = path
		c.RepoLanes = s.Lanes != 0
		c.merge(s)
		c.mergeRunners(s.Runners, path)
		c.Env = s.Env
	}
	if repo, ok := lookupRepo(f.Repos, root); ok {
		if repo.Env != nil {
			return nil, fmt.Errorf("%s (repo %s): env: belongs in a [repo PATH] section of %s, not config.yml", c.Path, root, filepath.Join(filepath.Dir(c.Path), "env.swim"))
		}
		c.HasRepo = true
		c.RepoLanes = c.RepoLanes || repo.Lanes != 0
		c.merge(repo)
		c.mergeRunners(repo.Runners, c.Path)
	}
	if c.Lanes == 0 {
		c.Lanes = DefaultLanes
	}
	// swim ci on a runner with no lanes setting counts the lane scripts and
	// passes that on to the lanes it starts (their swim hooks load config
	// again).
	if v := os.Getenv("SWIM_LANES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > c.Lanes {
			c.Lanes = n
		}
	}
	c.fillChimeDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s (repo %s): %w", c.Path, root, err)
	}
	return c, nil
}

// EnvKeysVar lists (space separated) the keys a lane's environment got
// from env files.
const EnvKeysVar = "SWIM_ENV_KEYS"

// RepoFileName is the committed per-repo settings file at the repo root.
const RepoFileName = "swim.yml"

// readRepoFile reads root's swim.yml: the same keys as a repos entry.
func readRepoFile(root string) (Settings, string, error) {
	var s Settings
	path := filepath.Join(root, RepoFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || root == "" {
		return s, "", nil
	}
	if err != nil {
		return s, "", err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return s, "", fmt.Errorf("parse %s: %w", path, err)
	}
	return s, path, nil
}

func readFile(path string) (*File, error) {
	f := &File{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

// lookupRepo matches root against repo keys, expanding ~ and cleaning paths.
func lookupRepo(repos map[string]Settings, root string) (Settings, bool) {
	for k, v := range repos {
		if sameRepo(k, root) {
			return v, true
		}
	}
	return Settings{}, false
}

// sameRepo reports whether a repos key names root (after ~ expansion,
// cleaning and resolving symlinks).
func sameRepo(key, root string) bool { return SameRepo(key, root) }

// SameRepo reports whether path (a repos key, an env.swim [repo PATH]
// section) names root, after ~ expansion, cleaning and resolving symlinks.
func SameRepo(key, root string) bool {
	canon := func(p string) string {
		p = filepath.Clean(expandHome(p))
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return p
	}
	return canon(key) == canon(root)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// mergeRunners adds runners, later sources winning per extension.
func (c *Config) mergeRunners(r map[string]string, from string) {
	for ext, cmd := range r {
		ext = strings.ToLower(strings.TrimPrefix(ext, "."))
		if strings.TrimSpace(cmd) == "" {
			continue
		}
		if c.Runners == nil {
			c.Runners, c.RunnerFrom = map[string]string{}, map[string]string{}
		}
		c.Runners[ext], c.RunnerFrom[ext] = strings.TrimSpace(cmd), from
	}
}

func (c *Config) merge(r Settings) {
	if r.Lanes != 0 {
		c.Lanes = r.Lanes
	}
	if r.HeaderEnv != nil {
		c.HeaderEnv = r.HeaderEnv
	}
	if r.Runtime != "" {
		c.Runtime = r.Runtime
	}
	if r.Toolchain != "" {
		c.Toolchain = r.Toolchain
	}
	if r.Deps != nil {
		c.Deps = r.Deps
	}
	if r.MaxParallel != nil {
		c.MaxParallel = r.MaxParallel
	}
	// Blocked commands add up: defaults, then the repo's (never removed).
	c.BlockedCommands = append(append([]string(nil), c.BlockedCommands...), r.BlockedCommands...)
	// Secret names add up too; the switches override.
	c.SecretEnv = append(append([]string(nil), c.SecretEnv...), r.SecretEnv...)
	c.SecretEnvIgnore = append(append([]string(nil), c.SecretEnvIgnore...), r.SecretEnvIgnore...)
	if r.SecretEnvAuto != nil {
		c.SecretEnvAuto = r.SecretEnvAuto
	}
	if r.SecretPatternsAuto != nil {
		c.SecretPatternsAuto = r.SecretPatternsAuto
	}
	if r.Chime != nil {
		c.Chime = r.Chime
	}
	if r.ChimeStyle != nil {
		c.ChimeStyle = r.ChimeStyle
	}
	if r.ChimeMinS != nil {
		c.ChimeMinS = r.ChimeMinS
	}
	if r.Interrupt != "" {
		c.Interrupt = r.Interrupt
	}
	if r.Approvals != "" {
		c.Approvals = r.Approvals
	}
	if r.OutputsKeep != "" {
		c.OutputsKeep = r.OutputsKeep
	}
	if r.MsgLogInterval != "" {
		c.MsgLogInterval = r.MsgLogInterval
	}
	if r.SigningKey != "" {
		c.SigningKey = r.SigningKey
	}
	if r.SecretsKey != "" {
		c.SecretsKey = r.SecretsKey
	}
	if r.Signing != "" {
		c.Signing = r.Signing
	}
	if r.InterruptGrace != "" {
		c.InterruptGrace = r.InterruptGrace
	}
	if r.TermGrace != "" {
		c.TermGrace = r.TermGrace
	}
}

// Validate checks lane numbers are within 1..Lanes and deps form no cycle.
func (c *Config) Validate() error {
	if c.Lanes < 1 || c.Lanes > 99 {
		return fmt.Errorf("lanes must be 1..99, got %d", c.Lanes)
	}
	for _, b := range c.BlockedCommands {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("blocked_commands: empty pattern")
		}
	}
	if err := c.validateInterrupt(); err != nil {
		return err
	}
	if c.MaxParallel != nil && *c.MaxParallel < 0 {
		return fmt.Errorf("max_parallel must be 0 (unlimited) or more, got %d", *c.MaxParallel)
	}
	if err := c.validateChime(); err != nil {
		return err
	}
	for lane, deps := range c.Deps {
		if !c.ValidLane(lane) {
			return fmt.Errorf("deps: swim %d is outside 1..%d", lane, c.Lanes)
		}
		for _, d := range deps {
			if !c.ValidLane(d) {
				return fmt.Errorf("deps: swim %d depends on swim %d, outside 1..%d", lane, d, c.Lanes)
			}
			if d == lane {
				return fmt.Errorf("deps: swim %d depends on itself", lane)
			}
		}
	}
	if cyc := c.findCycle(); cyc != nil {
		parts := make([]string, len(cyc))
		for i, n := range cyc {
			parts[i] = fmt.Sprint(n)
		}
		return fmt.Errorf("deps: cycle %s", strings.Join(parts, " -> "))
	}
	return nil
}

// Blocked returns the patterns no lane command may contain: the built-ins
// (git push, git commit, git pull) plus defaults' and this repo's additions.
func (c *Config) Blocked() []string { return policy.Patterns(c.BlockedCommands) }

// Masker returns the secret masker for env (usually os.Environ()).
func (c *Config) Masker(env []string) *redact.Masker {
	on := func(b *bool) bool { return b == nil || *b }
	names := c.SecretEnv
	// Values a lane got from env files are masked whatever their names
	// (the launcher lists those keys in SWIM_ENV_KEYS).
	for _, kv := range env {
		if keys, ok := strings.CutPrefix(kv, EnvKeysVar+"="); ok {
			names = append(append([]string(nil), names...), strings.Fields(keys)...)
		}
	}
	return redact.New(env, redact.Options{
		Names:        names,
		Auto:         on(c.SecretEnvAuto),
		Ignore:       c.SecretEnvIgnore,
		AutoPatterns: on(c.SecretPatternsAuto),
	})
}

// MsgLogEvery is msg_log_interval: the least time between two MSG lines a
// lane writes to its log (default 10s; 0 logs every change).
func (c *Config) MsgLogEvery() time.Duration {
	if c.MsgLogInterval == "" {
		return 10 * time.Second
	}
	d, err := time.ParseDuration(c.MsgLogInterval)
	if err != nil || d < 0 {
		return 10 * time.Second
	}
	return d
}

// OutputsKeepFor is outputs_keep: how long round outputs nothing uses are
// kept (default 14 days). Durations take d for days: 14d, 36h.
func (c *Config) OutputsKeepFor() time.Duration {
	if d, ok := parseDays(c.OutputsKeep); ok {
		return d
	}
	return 14 * 24 * time.Hour
}

// parseDays parses a duration that may use d (days): "14d", "1d12h".
func parseDays(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var days int
	if i := strings.Index(s, "d"); i > 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		days, s = n, s[i+1:]
	}
	var rest time.Duration
	if s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, false
		}
		rest = d
	}
	total := time.Duration(days)*24*time.Hour + rest
	return total, total > 0
}

// Parallel is the cap on lanes running at once (0 = unlimited).
func (c *Config) Parallel() int {
	if c.MaxParallel == nil {
		return 0
	}
	return *c.MaxParallel
}

// ValidLane reports whether n is a lane number for this repo.
func (c *Config) ValidLane(n int) bool { return n >= 1 && n <= c.Lanes }

// DepsOf returns the lanes n waits on, sorted.
func (c *Config) DepsOf(n int) []int {
	d := append([]int(nil), c.Deps[n]...)
	sort.Ints(d)
	return d
}

func (c *Config) findCycle() []int {
	const (
		unseen = iota
		active
		done
	)
	state := map[int]int{}
	var stack []int
	var visit func(n int) []int
	visit = func(n int) []int {
		state[n] = active
		stack = append(stack, n)
		for _, d := range c.DepsOf(n) {
			switch state[d] {
			case active:
				for i, s := range stack {
					if s == d {
						return append(append([]int(nil), stack[i:]...), d)
					}
				}
			case unseen:
				if cyc := visit(d); cyc != nil {
					return cyc
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = done
		return nil
	}
	for n := 1; n <= c.Lanes; n++ {
		if state[n] == unseen {
			if cyc := visit(n); cyc != nil {
				return cyc
			}
		}
	}
	return nil
}

// defaultFile is written by EnsureRepo when no config exists yet.
const defaultFile = `# swim configuration. Global defaults, overridden per repo (keyed by git toplevel).
defaults:
  lanes: 4
  # Env vars recorded in every step header (values printed; never list secrets).
  header_env: [STAGE, AWS_PROFILE, AWS_REGION]
  # Command whose output is recorded as the runtime version in step headers.
  # runtime: node --version
  # Shell line lane scripts run to load the pinned toolchain (non-interactive
  # shells don't load version managers).
  # toolchain: . "$HOME/.nvm/nvm.sh" && nvm use >/dev/null
repos: {}
`

// EnsureRepo creates the config file if missing and adds a section for root
// if none exists. Existing content and comments are preserved. It reports
// whether the file and the repo section were created.
func EnsureRepo(path, root string) (createdFile, addedRepo bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		createdFile = true
		data = []byte(defaultFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, false, err
		}
	} else if err != nil {
		return false, false, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return false, false, fmt.Errorf("%s: top level must be a mapping", path)
	}

	repos := mapGet(top, "repos")
	if repos == nil || repos.Kind != yaml.MappingNode {
		if repos == nil {
			top.Content = append(top.Content, scalar("repos"), &yaml.Node{Kind: yaml.MappingNode})
			repos = top.Content[len(top.Content)-1]
		} else {
			*repos = yaml.Node{Kind: yaml.MappingNode}
		}
	}
	repos.Style = 0 // render block style even if it was `{}`

	var f File
	_ = doc.Decode(&f)
	if _, ok := lookupRepo(f.Repos, root); ok {
		if createdFile {
			return true, false, writeNode(path, &doc)
		}
		return false, false, nil
	}

	entry := &yaml.Node{Kind: yaml.MappingNode}
	entry.Content = append(entry.Content, scalar("lanes"), scalar(fmt.Sprint(DefaultLanes)))
	deps := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	depsKey := scalar("deps")
	depsKey.HeadComment = "swim N starts after the listed lanes succeed, e.g. {2: [1], 4: [2]}"
	entry.Content = append(entry.Content, depsKey, deps)
	repos.Content = append(repos.Content, scalar(root), entry)
	return createdFile, true, writeNode(path, &doc)
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func writeNode(path string, doc *yaml.Node) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SetLanes sets `lanes` in root's repo section (creating the file or the
// section if needed), keeping the rest of the file and its comments.
func SetLanes(path, root string, lanes int) error {
	return SetKey(path, root, "lanes", fmt.Sprint(lanes), true)
}

// Interrupt defaults.
const (
	InterruptGraceful     = "graceful"  // first Ctrl-C: stop at the next step boundary
	InterruptImmediate    = "immediate" // first Ctrl-C: interrupt running steps
	DefaultInterruptGrace = 5 * time.Second
	DefaultTermGrace      = 10 * time.Second
)

func (c *Config) validateInterrupt() error {
	switch c.Approvals {
	case "", ApprovalsPrompt, ApprovalsFlags:
	default:
		return fmt.Errorf("approvals must be prompt or flags, got %q", c.Approvals)
	}
	switch c.Signing {
	case "", SigningAuto, SigningRequired, SigningOff:
	default:
		return fmt.Errorf("signing must be auto, required or off, got %q", c.Signing)
	}
	switch c.Interrupt {
	case "", InterruptGraceful, InterruptImmediate:
	default:
		return fmt.Errorf("interrupt must be graceful or immediate, got %q", c.Interrupt)
	}
	for _, kv := range [][2]string{{"interrupt_grace", c.InterruptGrace}, {"term_grace", c.TermGrace}} {
		if kv[1] == "" {
			continue
		}
		if d, err := time.ParseDuration(kv[1]); err != nil || d < 0 {
			return fmt.Errorf("%s must be a duration like 5s or 1m, got %q", kv[0], kv[1])
		}
	}
	return nil
}

// InterruptMode is what the first Ctrl-C does: graceful or immediate.
func (c *Config) InterruptMode() string {
	if c.Interrupt == "" {
		return InterruptGraceful
	}
	return c.Interrupt
}

// Graces returns how long a force quit waits before SIGKILL, and how long
// SIGTERM's graceful stop waits before forcing.
func (c *Config) Graces() (interrupt, term time.Duration) {
	interrupt, term = DefaultInterruptGrace, DefaultTermGrace
	if d, err := time.ParseDuration(c.InterruptGrace); err == nil {
		interrupt = d
	}
	if d, err := time.ParseDuration(c.TermGrace); err == nil {
		term = d
	}
	return interrupt, term
}

// Approval modes: what a guard does when its flag isn't set.
const (
	ApprovalsPrompt = "prompt" // ask the operator at the terminal (dry run if no one can be asked)
	ApprovalsFlags  = "flags"  // never ask: only FLAG=1 approves
)

// ApprovalMode is prompt (the default) or flags.
func (c *Config) ApprovalMode() string {
	if c.Approvals == "" {
		return ApprovalsPrompt
	}
	return c.Approvals
}

// Signing modes: whether the runner signs its chain entries.
const (
	SigningAuto     = "auto"     // sign when a key loads; record unsigned (still linked) when none does
	SigningRequired = "required" // refuse to start a round without a key
	SigningOff      = "off"      // never sign
)

// SigningMode is auto (the default), required or off.
func (c *Config) SigningMode() string {
	if c.Signing == "" {
		return SigningAuto
	}
	return c.Signing
}

// SecretsKeyPath is the SSH key swim secret encrypts to: secrets_key, else
// signing_key, else "" (secrets.LoadKey then tries ~/.ssh/id_ed25519).
func (c *Config) SecretsKeyPath() string {
	if c.SecretsKey != "" {
		return c.SecretsKey
	}
	return c.SigningKey
}
