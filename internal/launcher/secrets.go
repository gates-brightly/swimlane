package launcher

import (
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

// usesSecretsRE spots a script that gets secrets at run time: _swim.<name>
// in a step, swim secret get, or ["swim", "secret", "get", ...].
var usesSecretsRE = regexp.MustCompile(`_swim\.[A-Za-z]|\bsecret\s+get\b|['"]secret['"]\s*,\s*['"]get['"]`)

// openSecrets decrypts the secrets the run's repo sees and starts their
// broker; nil when there are none and no lane asks for any. The key's
// passphrase is asked only when a selected lane uses secrets.
func openSecrets(o *Options, sel []int) (*runSecrets, error) {
	declared := map[int][]lane.SecretRef{}
	uses := false
	for _, n := range sel {
		info, _ := lane.ReadScript(o.Root, n)
		if len(info.Secrets) > 0 {
			declared[n] = info.Secrets
			uses = true
		} else if data, err := lane.ScriptBytes(o.Root, n); err == nil && usesSecretsRE.Match(data) {
			uses = true
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
	if f.Empty(repo) && !envSupplied && !uses {
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
	for n, refs := range declared {
		for _, ref := range refs {
			if _, err := vault.Get(ref.Name); err != nil {
				if openErr != nil {
					err = openErr
				}
				return nil, fmt.Errorf("swim %d: Secrets: %s: %v", n, ref.Name, err)
			}
		}
	}
	rs := &runSecrets{vault: vault, used: map[string]bool{}}
	if openErr != nil && uses {
		rs.note = "swim: secrets: " + openErr.Error() + "; lanes get only SWIM_SECRET_<NAME> this run"
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

// env is lane n's broker address and its Secrets: variables.
func (rs *runSecrets) env(o Options, n int) []string {
	env := rs.srv.Env()
	info, _ := lane.ReadScript(o.Root, n)
	for _, ref := range info.Secrets {
		s, err := rs.vault.Get(ref.Name)
		if err != nil {
			continue // checked at start; gone since only if the env changed
		}
		env = append(env, ref.Env+"="+s.Value)
		rs.record(&o, n, s, "Secrets: "+ref.Env)
	}
	return env
}
