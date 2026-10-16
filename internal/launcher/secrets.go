package launcher

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/secrets"
)

// runSecrets is a run's secrets: the vault decrypted at start and the
// broker its lanes ask. A guarded secret (swim.yml's secrets:, a hub
// grant's guard) goes to a lane only once the launcher recorded an
// approval of its guard for that lane process (runAuthority.approved).
type runSecrets struct {
	vault    *secrets.Vault
	srv      *secrets.Server
	policies map[string]secrets.Policy
	note     string // why the store couldn't be opened, for the live view

	mu   sync.Mutex
	used map[string]bool // lane/name/how already in .swim.log
	// The approval broker, which says which lane process asks, and the
	// authority, which says what it approved: set once they start.
	ap   *approval.Server
	auth *runAuthority
	disp *display.Display
}

// UsesSecretsRE spots a script that gets secrets: swim secret get NAME
// (bash, or ["swim", "secret", "get", ...]), or an SDK's secret.get /
// Secret.Get. A regular expression, kept: it reads any language's source,
// and a miss only means the secrets broker isn't started early.
var UsesSecretsRE = regexp.MustCompile(`\bsecret\s+get\b|['"]secret['"]\s*,\s*['"]get['"]|\bsecret\.get\(|\bSecret\.Get\(`)

// openSecrets decrypts the secrets the run's repo sees and starts their
// broker; nil when there are none. The key's passphrase is asked only when
// a selected lane may use secrets (a compiled lane can't be read: it may).
func openSecrets(o *Options, sel []int) (*runSecrets, error) {
	uses := false
	for _, n := range sel {
		data, err := lane.ScriptBytes(o.Root, n)
		if err == nil && (UsesSecretsRE.Match(data) || bytes.IndexByte(data, 0) >= 0) {
			uses = true
			break
		}
	}
	envSupplied := false
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, secrets.EnvPrefix) {
			envSupplied = true
			break
		}
	}
	path := secrets.Path(config.Path())
	repo := secrets.RepoKey(o.Root)
	f, err := secrets.Load(path)
	if err != nil {
		return nil, err
	}
	var granted map[string]secrets.Granted
	var hubErr error
	if uses && o.HubSecrets != nil {
		granted, hubErr = o.HubSecrets()
	}
	if f.Empty(repo) && !envSupplied && len(granted) == 0 && hubErr == nil {
		return nil, nil
	}
	keyPath := o.Cfg.SecretsKeyPath()
	var prompt func() ([]byte, error)
	if uses && o.SecretsPrompt != nil && !f.Empty(repo) {
		if k, err := secrets.LoadKey(keyPath); err == nil {
			prompt = o.SecretsPrompt(k.Path)
		}
	}
	vault, _, openErr := secrets.OpenStore(path, repo, keyPath, prompt, os.Getenv)
	vault.Add(secrets.ScopeHub, secrets.Values(granted))
	rs := &runSecrets{vault: vault, used: map[string]bool{}, policies: Policies(o.Cfg, vault, granted)}
	var notes []string
	switch {
	case openErr != nil && uses && !f.Empty(repo):
		notes = append(notes, "swim: secrets: "+openErr.Error()+"; this run's lanes get the hub's grants and SWIM_SECRET_<NAME> only")
	case hubErr != nil:
		notes = append(notes, "swim: "+hubErr.Error()+"; lanes get only local secrets this run")
	}
	// A key with no passphrase lets a lane decrypt secrets.yml itself,
	// guarded secrets included: say so (swim doctor does too).
	if len(o.Cfg.GuardedSecrets()) > 0 && !f.Empty(repo) {
		if k, err := secrets.LoadKey(keyPath); err == nil && secrets.Unprotected(k) {
			notes = append(notes, "swim: warning: the secrets key "+k.Path+" has no passphrase: a lane can decrypt secrets.yml itself, guarded secrets included (ssh-keygen -p -f "+k.Path+")")
		}
	}
	rs.note = strings.Join(notes, "\n")
	srv, err := secrets.Serve(vault, rs.policies, secrets.Hooks{
		Identify: rs.identify(o),
		Approved: rs.approved,
		Used:     func(c secrets.Caller, s secrets.Secret, how string) { rs.record(o, c, s, how) },
		Released: func(c secrets.Caller, s secrets.Secret, by []secrets.Approval) { rs.released(o, c, s, by) },
		Refused:  func(c secrets.Caller, name string, flags []string, why string) { rs.refused(o, c, name, flags, why) },
		Check:    lane.CheckMode(),
	})
	if err != nil {
		return nil, err
	}
	rs.srv = srv
	return rs, nil
}

// Policies are the run's guarded secrets: swim.yml's secrets:, and the
// guard a hub's admin set on a grant the vault resolves from the hub. A
// secret guarded by both needs both approved.
func Policies(cfg *config.Config, vault *secrets.Vault, granted map[string]secrets.Granted) map[string]secrets.Policy {
	out := map[string]secrets.Policy{}
	for name, p := range cfg.GuardedSecrets() {
		out[name] = secrets.Policy{Guards: [][]string{p.Guard}, Step: p.Release == config.ReleaseStep}
	}
	for name, g := range granted {
		if len(g.Guard) == 0 {
			continue
		}
		if s, err := vault.Get(name); err != nil || s.Scope != secrets.ScopeHub {
			continue // a local secret of that name wins over the grant
		}
		p := out[name]
		p.Guards = append(p.Guards, g.Guard)
		out[name] = p
	}
	return out
}

// attach gives the broker the run's approval broker and authority, once
// they've started (before any lane does).
func (rs *runSecrets) attach(ap *approval.Server, auth *runAuthority, disp *display.Display) {
	if rs == nil {
		return
	}
	rs.mu.Lock()
	rs.ap, rs.auth, rs.disp = ap, auth, disp
	rs.mu.Unlock()
}

// identify says which lane process sent a request: the approval broker's
// registration for its token, checked against the connection's process.
func (rs *runSecrets) identify(o *Options) func(net.Conn, string) (secrets.Caller, string) {
	return func(conn net.Conn, tok string) (secrets.Caller, string) {
		rs.mu.Lock()
		ap := rs.ap
		rs.mu.Unlock()
		if ap == nil {
			return secrets.Caller{}, "this run has no approval broker to say which lane asks"
		}
		c, why := ap.Identify(conn, tok)
		if why != "" {
			return secrets.Caller{}, why
		}
		job := c.Job
		if job == "" {
			job = runningJob(*o, c.Lane, c.Cell)
		}
		return secrets.Caller{Lane: c.Lane, Cell: c.Cell, Job: job}, ""
	}
}

// approved says whether flag was approved for lane process c.
func (rs *runSecrets) approved(c secrets.Caller, flag string) (secrets.Approval, bool) {
	rs.mu.Lock()
	auth := rs.auth
	rs.mu.Unlock()
	if auth == nil {
		return secrets.Approval{}, false
	}
	return auth.approved(c.Key, flag)
}

// record puts a lane's use of a secret in .swim.log, once per lane, name
// and way: never its value.
func (rs *runSecrets) record(o *Options, c secrets.Caller, s secrets.Secret, how string) {
	if lane.CheckMode() {
		return
	}
	k := fmt.Sprintf("%d/%s/%s", c.Lane, s.Name, how)
	rs.mu.Lock()
	seen := rs.used[k]
	rs.used[k] = true
	rs.mu.Unlock()
	if !seen {
		history.Log(o.Root, history.Entry{Event: history.SecretUse, Lane: c.Lane, Run: o.RunID, Detail: fmt.Sprintf("%s (%s scope, %s)", s.Name, s.Scope, how)})
	}
}

// released records a guarded secret handed to a lane: the chain (name,
// flag, how it was approved, the approve entry; never the value) and
// .swim.log.
func (rs *runSecrets) released(o *Options, c secrets.Caller, s secrets.Secret, by []secrets.Approval) {
	var flags, vias, entries []string
	for _, a := range by {
		flags = append(flags, a.Flag)
		vias = append(vias, a.Via)
		if a.Entry != "" {
			entries = append(entries, a.Entry)
		}
	}
	e := chain.Entry{Type: "secret-released", Run: o.RunID, Lane: c.Lane, Job: c.Job, Secret: s.Name,
		Guard: strings.Join(flags, " "), Method: strings.Join(vias, " "), Approvals: entries}
	rs.chain(o, c, e)
	history.Log(o.Root, history.Entry{Event: history.SecretReleased, Lane: c.Lane, Job: c.Job, Run: o.RunID,
		Detail: fmt.Sprintf("%s (guarded by %s, approved by %s)", s.Name, strings.Join(flags, ", "), strings.Join(vias, ", "))})
}

// refused records a guarded secret refused to a lane: the chain,
// .swim.log and the live view.
func (rs *runSecrets) refused(o *Options, c secrets.Caller, name string, flags []string, why string) {
	if lane.CheckMode() {
		return
	}
	e := chain.Entry{Type: "secret-refused", Run: o.RunID, Lane: c.Lane, Job: c.Job, Secret: name,
		Guard: strings.Join(flags, " "), Action: why}
	rs.chain(o, c, e)
	history.Log(o.Root, history.Entry{Event: history.SecretRefused, Lane: c.Lane, Job: c.Job, Run: o.RunID, Detail: name + "  (" + why + ")"})
	rs.mu.Lock()
	disp := rs.disp
	rs.mu.Unlock()
	if disp != nil {
		who := fmt.Sprintf("swim %d", c.Lane)
		if c.Lane == 0 {
			who = "a process without a lane token"
		}
		disp.Message(fmt.Sprintf("swim: secret %s refused to %s: %s", name, who, why))
	}
}

// chain records a secret entry with the lane's manifest, signed by the
// runner's key.
func (rs *runSecrets) chain(o *Options, c secrets.Caller, e chain.Entry) {
	if lane.CheckMode() {
		return
	}
	if m := o.manifests[c.Lane]; m != nil {
		e.Script, e.Manifest = m.ScriptSHA, m.SHA256
	}
	if _, err := authority.Record(o.Root, o.Cfg, e); err != nil {
		fmt.Fprintln(os.Stderr, "swim: secrets: "+err.Error())
	}
}

// env is what lane n needs to reach the broker.
func (rs *runSecrets) env(o Options, n int) []string { return rs.srv.Env() }

// labeled is every one of the run's secrets, for the launcher's own
// masking of the lanes' output (it holds them all).
func (rs *runSecrets) labeled() []redact.Labeled {
	if rs == nil {
		return nil
	}
	var out []redact.Labeled
	for _, s := range rs.vault.All() {
		out = append(out, redact.Labeled{Name: s.Name, Value: s.Value})
	}
	return out
}

// stripGuarded removes the SWIM_SECRET_<NAME> stand-ins of guarded secrets
// from a lane's environment: the broker serves them (once approved), the
// lane's environment never carries them.
func (rs *runSecrets) stripGuarded(env []string) []string {
	if rs == nil || len(rs.policies) == 0 {
		return env
	}
	out := env[:0:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if n, ok := strings.CutPrefix(k, secrets.EnvPrefix); ok && rs.guardedEnv(n) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (rs *runSecrets) guardedEnv(upper string) bool {
	for name, p := range rs.policies {
		if p.Guarded() && secrets.EnvName(name) == upper {
			return true
		}
	}
	return false
}
