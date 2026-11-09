// Package config loads ~/.config/swim/config.yml: global defaults, then
// per-repo and per-branch sections keyed by the repo's name (repos:
// swimlane, swimlane/main). Nothing in a repo configures swim.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/policy"
	"github.com/gates-brightly/swimlane/internal/redact"
)

// Settings is one block of configuration: `defaults`, or a repos entry
// (a repo's or a branch's). Zero values mean "not set", so a later block
// overrides field by field.
type Settings struct {
	// Lanes is the highest lane in use (see Config.Active), not a setting:
	// swim finds a repo's lanes on disk (lane.Associated).
	Lanes           int      `yaml:"-"`
	HeaderEnv       []string `yaml:"header_env,omitempty"`
	Runtime         string   `yaml:"runtime,omitempty"`
	Toolchain       string   `yaml:"toolchain,omitempty"`
	MaxParallel     *int     `yaml:"max_parallel,omitempty"`     // lanes running at once; 0 = unlimited
	BlockedCommands []string `yaml:"blocked_commands,omitempty"` // lane commands may not contain these

	SecretEnv          []string `yaml:"secret_env,omitempty"`           // values of these variables are masked (***)
	SecretEnvAuto      *bool    `yaml:"secret_env_auto,omitempty"`      // also *_TOKEN, *_SECRET, *_PASSWORD, ... (default true)
	SecretEnvIgnore    []string `yaml:"secret_env_ignore,omitempty"`    // names exempt from secret_env_auto
	SecretPatternsAuto *bool    `yaml:"secret_patterns_auto,omitempty"` // also AWS key ids, GitHub/Slack tokens, JWTs (default true)
	Interrupt          string   `yaml:"interrupt,omitempty"`            // graceful (default) | immediate: what the first Ctrl-C does
	SigningKey         string   `yaml:"signing_key,omitempty"`          // SSH key the runner signs the chain with (default ~/.ssh/id_ed25519)
	Signing            string   `yaml:"signing,omitempty"`              // auto (default: sign if a key loads) | required | off
	InterruptGrace     string   `yaml:"interrupt_grace,omitempty"`      // force quit: SIGINT, then SIGKILL after this (default 5s)
	TermGrace          string   `yaml:"term_grace,omitempty"`           // SIGTERM: graceful, then force quit after this (default 10s)
	QueueLease         string   `yaml:"queue_lease,omitempty"`          // a queue worker's heartbeat holds this long (default 2m)
	ClaimLease         string   `yaml:"claim_lease,omitempty"`          // a lane claim (swim new takes it) lasts this long unless refreshed (default 2h)
	WatchBatches       int      `yaml:"watch_batches,omitempty"`        // batches one swim watch runs at once (default 4)
	Notify             *bool    `yaml:"notify,omitempty"`               // terminal notifications and a progress bar (OSC 777, OSC 9;4; default off)
	NotifyURL          string   `yaml:"notify_url,omitempty"`           // ntfy-style URL each notification is POSTed to (with curl)
	NotifyCommand      string   `yaml:"notify_command,omitempty"`       // sh -c this for each notification instead, with SWIM_EVENT, SWIM_TITLE, ...
	Phone              string   `yaml:"phone,omitempty"`                // tailnet device (name or IP) whose taps on a question's links answer it in swim watch
	PhoneExpiry        string   `yaml:"phone_expiry,omitempty"`         // how long a question's phone links work (default 15m)
	// Runners maps a lane script's extension (ts, py, ...) to the command
	// that runs it ("tsx", "/usr/local/bin/python3"). A script's #! line
	// wins; swim asks the operator for one the first time it meets an
	// extension with neither, and saves the answer in the repo's section.
	Runners map[string]string `yaml:"runners,omitempty"`
}

// File is the on-disk shape of config.yml.
type File struct {
	Defaults Settings            `yaml:"defaults"`
	Repos    map[string]Settings `yaml:"repos"`
}

// The layers a value can come from, lowest first: swim's built-in
// default, config.yml's defaults:, the repo's repos entry, then the
// branch's (repos.NAME/BRANCH).
const (
	LayerDefault  = "default"
	LayerDefaults = "defaults"
	LayerRepo     = "repo"
	LayerBranch   = "repo/branch"
)

// Layer is one section of config.yml that applies, with its settings.
type Layer struct {
	Name    string // LayerDefaults, LayerRepo or LayerBranch
	Section string // its key: "" for defaults:, else the repos key (swimlane, swimlane/main)
	Found   bool   // the file has the section
	Settings
}

// Config is the effective configuration for one repo.
type Config struct {
	Path   string // config.yml location
	Root   string // repo root the settings apply to
	Repo   string // the repo's name, its repos key (RepoName)
	Branch string // the checked-out branch ("" on a detached HEAD)
	// Layers are the sections that apply, lowest first: defaults, the
	// repo's, then the branch's (when there is a branch).
	Layers []Layer
	Settings
	// Active are the lanes the repo is using (lane.Associated): the ones
	// swim shows. Lanes (in Settings) is the highest of them (at least 1),
	// so loops over 1..Lanes see every one.
	Active []int
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

// Load reads the config file and resolves the effective settings for
// root: swim's defaults, then defaults:, then repos.NAME, then
// repos.NAME/BRANCH, each later one winning key by key. A missing file is
// not an error: built-in defaults apply.
func Load(root string) (*Config, error) {
	c, err := Read(root)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s (repo %s): %w", c.Path, root, err)
	}
	return c, nil
}

// Read is Load without checking the values: what swim config's screen
// shows, so a bad value can be fixed there.
func Read(root string) (*Config, error) {
	c := &Config{Path: Path(), Root: root, Repo: RepoName(root), Branch: RepoBranch(root)}
	f, err := readFile(c.Path)
	if err != nil {
		return nil, err
	}
	c.Layers = []Layer{{Name: LayerDefaults, Found: true, Settings: f.Defaults}}
	repo, ok := f.Repos[c.Repo]
	c.Layers = append(c.Layers, Layer{Name: LayerRepo, Section: c.Repo, Found: ok, Settings: repo})
	if c.Branch != "" {
		key := c.Repo + "/" + c.Branch
		br, ok := f.Repos[key]
		c.Layers = append(c.Layers, Layer{Name: LayerBranch, Section: key, Found: ok, Settings: br})
	}
	for _, l := range c.Layers {
		c.merge(l.Settings)
	}
	// The lanes in use, not a setting.
	c.Active = lane.Associated(root)
	c.Lanes = 1
	if len(c.Active) > 0 {
		c.Lanes = c.Active[len(c.Active)-1]
	}
	// A run passes its lane count on to the lanes it starts (their swim
	// hooks load config again).
	if v := os.Getenv("SWIM_LANES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > c.Lanes {
			c.Lanes = n
		}
	}
	return c, nil
}

// EnvKeysVar lists (space separated) the keys a lane's environment got
// from env files.
const EnvKeysVar = "SWIM_ENV_KEYS"

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

// PathKeys are the repos: keys of the config file at path that are paths
// (/abs/path, ~/src/x): swim keyed repos by their git toplevel once; it
// keys them by name now, so these match nothing.
func PathKeys(path string) ([]string, error) {
	f, err := readFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for k := range f.Repos {
		if filepath.IsAbs(k) || k == "~" || strings.HasPrefix(k, "~/") {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out, nil
}

// merge lays r over the settings so far: a key r sets wins, except the
// lists that add up and runners, which win per extension.
func (c *Config) merge(r Settings) {
	for ext, cmd := range r.Runners {
		ext = strings.ToLower(strings.TrimPrefix(ext, "."))
		if strings.TrimSpace(cmd) == "" {
			continue
		}
		if c.Runners == nil {
			c.Runners = map[string]string{}
		}
		c.Runners[ext] = strings.TrimSpace(cmd)
	}
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
	if r.Interrupt != "" {
		c.Interrupt = r.Interrupt
	}
	if r.QueueLease != "" {
		c.QueueLease = r.QueueLease
	}
	if r.ClaimLease != "" {
		c.ClaimLease = r.ClaimLease
	}
	if r.WatchBatches != 0 {
		c.WatchBatches = r.WatchBatches
	}
	if r.Notify != nil {
		c.Notify = r.Notify
	}
	if r.NotifyURL != "" {
		c.NotifyURL = r.NotifyURL
	}
	if r.NotifyCommand != "" {
		c.NotifyCommand = r.NotifyCommand
	}
	if r.Phone != "" {
		c.Phone = r.Phone
	}
	if r.PhoneExpiry != "" {
		c.PhoneExpiry = r.PhoneExpiry
	}
	if r.SigningKey != "" {
		c.SigningKey = r.SigningKey
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

// Validate checks the values settings must have.
func (c *Config) Validate() error {
	if c.Lanes < 1 || c.Lanes > lane.MaxLanes {
		return fmt.Errorf("lanes must be 1..%d, got %d", lane.MaxLanes, c.Lanes)
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
	return nil
}

// Blocked returns the patterns no lane command may contain: defaults' and
// this repo's blocked_commands. swim blocks nothing by itself.
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

// QueueLeaseFor is queue_lease: how long a queue worker's heartbeat holds
// (0: the queue's default, 2m). A worker renews it every third of that;
// from another host, a claim whose heartbeat is older is orphaned.
func (c *Config) QueueLeaseFor() time.Duration {
	if d, err := time.ParseDuration(c.QueueLease); err == nil && d >= 10*time.Second {
		return d
	}
	return 0
}

// ClaimLeaseFor is claim_lease: how long a lane claim lasts unless
// refreshed (default 2h).
func (c *Config) ClaimLeaseFor() time.Duration {
	if d, err := time.ParseDuration(c.ClaimLease); err == nil && d >= time.Minute {
		return d
	}
	return lane.DefaultClaimLease
}

// DefaultPhoneExpiry is how long a question's phone links work unless
// phone_expiry says otherwise.
const DefaultPhoneExpiry = 15 * time.Minute

// PhoneExpiryFor is phone_expiry: how long after a question is asked its
// phone links still answer it.
func (c *Config) PhoneExpiryFor() time.Duration {
	if d, err := time.ParseDuration(c.PhoneExpiry); err == nil && d >= time.Minute {
		return d
	}
	return DefaultPhoneExpiry
}

// NotifyOn is notify: whether swim run and swim watch send the terminal
// notifications (a question waiting, a run or batch ending) and draw its
// progress bar (package notify). Off unless set.
func (c *Config) NotifyOn() bool { return c.Notify != nil && *c.Notify }

// DefaultWatchBatches is how many batches one swim watch runs at once
// unless watch_batches or --batches says otherwise.
const DefaultWatchBatches = 4

// WatchBatchesFor is watch_batches: how many queued batches one swim watch
// runs at once (batches whose lanes don't overlap).
func (c *Config) WatchBatchesFor() int {
	if c.WatchBatches >= 1 {
		return c.WatchBatches
	}
	return DefaultWatchBatches
}

// Parallel is the cap on lanes running at once (0 = unlimited).
func (c *Config) Parallel() int {
	if c.MaxParallel == nil {
		return 0
	}
	return *c.MaxParallel
}

// ValidLane reports whether n is a lane number (1..99): any lane can be
// started; the ones in use are Active.
func (c *Config) ValidLane(n int) bool { return n >= 1 && n <= laneMax }

// laneMax is lane.MaxLanes (named apart from Validate's `lane` variables).
const laneMax = lane.MaxLanes

// IsActive reports whether lane n is in use (see Active).
func (c *Config) IsActive(n int) bool { return slices.Contains(c.Active, n) }

// defaultFile is written by EnsureFile when no config exists yet.
const defaultFile = `# swim configuration: defaults for every repo, then repos: sections keyed
# by the repo's name (its origin URL's last part: swimlane) or by
# name/branch (swimlane/main). Later wins, key by key:
# defaults < repos.NAME < repos.NAME/BRANCH.
defaults:
  # Env vars recorded in every round header when set (values printed; never
  # list secrets), e.g. [STAGE, AWS_PROFILE, AWS_REGION].
  header_env: []
  # Command whose output is recorded as the runtime version in step headers.
  # runtime: node --version
  # Shell line lane scripts run to load the pinned toolchain (non-interactive
  # shells don't load version managers).
  # toolchain: . "$HOME/.nvm/nvm.sh" && nvm use >/dev/null
repos: {}
`

// EnsureFile creates the config file with swim's starting content if it is
// missing, and reports whether it did. An existing file is left alone.
func EnsureFile(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(defaultFile), 0o644)
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

// Interrupt defaults.
const (
	InterruptGraceful     = "graceful"  // first Ctrl-C: stop at the next step boundary
	InterruptImmediate    = "immediate" // first Ctrl-C: interrupt running steps
	DefaultInterruptGrace = 5 * time.Second
	DefaultTermGrace      = 10 * time.Second
)

func (c *Config) validateInterrupt() error {
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
	if c.QueueLease != "" {
		if d, err := time.ParseDuration(c.QueueLease); err != nil || d < 10*time.Second {
			return fmt.Errorf("queue_lease must be a duration of 10s or more, like 2m, got %q", c.QueueLease)
		}
	}
	if c.ClaimLease != "" {
		if d, err := time.ParseDuration(c.ClaimLease); err != nil || d < time.Minute {
			return fmt.Errorf("claim_lease must be a duration of 1m or more, like 2h, got %q", c.ClaimLease)
		}
	}
	if c.PhoneExpiry != "" {
		if d, err := time.ParseDuration(c.PhoneExpiry); err != nil || d < time.Minute {
			return fmt.Errorf("phone_expiry must be a duration of 1m or more, like 15m, got %q", c.PhoneExpiry)
		}
	}
	if c.WatchBatches < 0 {
		return fmt.Errorf("watch_batches must be 1 or more, got %d", c.WatchBatches)
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
