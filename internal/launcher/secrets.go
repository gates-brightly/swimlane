package launcher

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/secrets"
)

// runSecrets is a run's secrets: the vault decrypted at start and the
// broker its lanes ask.
type runSecrets struct {
	vault *secrets.Vault
	srv   *secrets.Server
	note  string // why the store couldn't be opened, for the live view

	mu   sync.Mutex
	used map[string]bool // lane/name/how already in .swim.log
}

// UsesSecretsRE spots a script that gets secrets: swim secret get NAME
// (bash, or ["swim", "secret", "get", ...]), or an SDK's secret.get /
// Secret.Get.
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
	var granted map[string]string
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
	vault.Add(secrets.ScopeHub, granted)
	rs := &runSecrets{vault: vault, used: map[string]bool{}}
	switch {
	case openErr != nil && uses && !f.Empty(repo):
		rs.note = "swim: secrets: " + openErr.Error() + "; this run's lanes get the hub's grants and SWIM_SECRET_<NAME> only"
	case hubErr != nil:
		rs.note = "swim: " + hubErr.Error() + "; lanes get only local secrets this run"
	}
	srv, err := secrets.Serve(vault, func(n int, s secrets.Secret, how string) { rs.record(o, n, s, how) })
	if err != nil {
		return nil, err
	}
	rs.srv = srv
	return rs, nil
}

// record puts a lane's use of a secret in .swim.log, once per lane, name
// and way: never its value.
func (rs *runSecrets) record(o *Options, n int, s secrets.Secret, how string) {
	if lane.CheckMode() {
		return
	}
	k := fmt.Sprintf("%d/%s/%s", n, s.Name, how)
	rs.mu.Lock()
	seen := rs.used[k]
	rs.used[k] = true
	rs.mu.Unlock()
	if !seen {
		history.Log(o.Root, history.Entry{Event: history.SecretUse, Lane: n, Run: o.RunID, Detail: fmt.Sprintf("%s (%s scope, %s)", s.Name, s.Scope, how)})
	}
}

// env is what lane n needs to reach the broker.
func (rs *runSecrets) env(o Options, n int) []string { return rs.srv.Env() }
