package config

import (
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/secrets"
)

// SecretPolicy is a secret's entry in swim.yml's secrets: a guarded secret,
// which the run's broker hands a lane only once one of its guard flags was
// approved for that lane in this run.
//
//	secrets:
//	  PROD_DB_ADMIN_URL: { guard: ORDERS_ALLOW_DROP }
//	  AWS_PROD_WRITE_ROLE: { guard: [INFRA_APPLY, INFRA_DESTROY], release: step }
type SecretPolicy struct {
	// Guard are the flags that release it (any one of them).
	Guard Flags `yaml:"guard,omitempty"`
	// Release is lane (the default: until the lane exits) or step (handed
	// out once: a second get is refused).
	Release string `yaml:"release,omitempty"`
}

// Release values.
const (
	ReleaseLane = "lane"
	ReleaseStep = "step"
)

// Flags is one guard flag or a list of them.
type Flags []string

// UnmarshalYAML accepts a single flag or a list.
func (f *Flags) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*f = Flags{n.Value}
		return nil
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*f = l
	return nil
}

var flagRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateSecrets checks swim.yml's secrets: every name a secret name,
// every guard a flag, release lane or step.
func validateSecrets(m map[string]SecretPolicy) error {
	for _, name := range sortedKeys(m) {
		p := m[name]
		if !secrets.ValidName(name) {
			return fmt.Errorf("secrets: %q isn't a secret name", name)
		}
		if len(p.Guard) == 0 {
			return fmt.Errorf("secrets: %s needs a guard: (a flag, or a list of them)", name)
		}
		for _, g := range p.Guard {
			if !flagRE.MatchString(g) {
				return fmt.Errorf("secrets: %s: %q isn't a guard flag", name, g)
			}
		}
		switch p.Release {
		case "", ReleaseLane, ReleaseStep:
		default:
			return fmt.Errorf("secrets: %s: release: %q, want lane or step", name, p.Release)
		}
	}
	return nil
}

// GuardedSecrets are the secrets swim.yml guards, by name (nil for none).
func (c *Config) GuardedSecrets() map[string]SecretPolicy { return c.Repo.Secrets }

// SecretGuard is the guard flags of secret name (none if it isn't
// guarded), matching any spelling of its SWIM_SECRET_<NAME> variable.
func (c *Config) SecretGuard(name string) []string {
	if p, ok := c.Repo.Secrets[name]; ok {
		return p.Guard
	}
	for n, p := range c.Repo.Secrets {
		if secrets.EnvName(n) == secrets.EnvName(name) {
			return p.Guard
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidFlag reports whether s can be a guard flag (a shell variable name).
func ValidFlag(s string) bool { return flagRE.MatchString(s) }
