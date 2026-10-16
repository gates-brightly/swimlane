package config

import (
	"crypto/sha256"
	"encoding/hex"

	"gopkg.in/yaml.v3"
)

// PolicyVersion numbers the list of swim.yml keys PolicyHash covers. Adding
// a key changes every lane's manifest (and so stales every approval bound
// to one), so it is deliberate: add it to PolicyKeys and bump this.
const PolicyVersion = 3

// PolicyKeys are the swim.yml keys that change what a lane may do, or how
// its approvals and secrets work: an approval bound to a manifest stops
// counting when any of them changes.
var PolicyKeys = []string{"blocked_commands", "runners", "approvals", "signing",
	"secret_env", "secret_env_auto", "secret_env_ignore", "secret_patterns_auto", "env",
	"secrets", "ambient_credentials", "modules"}

// policySubset is the part of swim.yml PolicyHash covers, in PolicyKeys order.
type policySubset struct {
	Version            int               `yaml:"version"`
	BlockedCommands    []string          `yaml:"blocked_commands"`
	Runners            map[string]string `yaml:"runners"`
	Approvals          string            `yaml:"approvals"`
	Signing            string            `yaml:"signing"`
	SecretEnv          []string          `yaml:"secret_env"`
	SecretEnvAuto      *bool             `yaml:"secret_env_auto"`
	SecretEnvIgnore    []string          `yaml:"secret_env_ignore"`
	SecretPatternsAuto *bool             `yaml:"secret_patterns_auto"`
	Env                map[string]string `yaml:"env"`
	// Version 2: guarded secrets.
	Secrets            map[string]SecretPolicy `yaml:"secrets"`
	AmbientCredentials []string                `yaml:"ambient_credentials"`
	// Version 3: which modules lanes may use.
	Modules *ModulesPolicy `yaml:"modules"`
}

// PolicyHash is the SHA-256 of the policy keys swim.yml sets. Only the
// committed swim.yml counts: an operator's own config.yml differs from
// machine to machine, and an approval signed on one must hold on another.
// Keys outside PolicyKeys (max_parallel, chime, ...) never change it.
func (c *Config) PolicyHash() string {
	r := c.Repo
	data, _ := yaml.Marshal(policySubset{Version: PolicyVersion, BlockedCommands: r.BlockedCommands, Runners: r.Runners,
		Approvals: r.Approvals, Signing: r.Signing, SecretEnv: r.SecretEnv, SecretEnvAuto: r.SecretEnvAuto,
		SecretEnvIgnore: r.SecretEnvIgnore, SecretPatternsAuto: r.SecretPatternsAuto, Env: r.Env,
		Secrets: r.Secrets, AmbientCredentials: r.AmbientCredentials, Modules: r.Modules})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
