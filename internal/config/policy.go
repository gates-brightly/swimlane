package config

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

// PolicyVersion numbers the set of swim.yml keys swim treats as policy. It
// is recorded in each manifest, but no longer hashed: PolicyHash covers only
// the keys a repo sets, so a key swim adds changes nothing for a repo that
// doesn't set it, and upgrading swim never stales an approval.
const PolicyVersion = 3

// PolicyKeys are the swim.yml keys that change what a lane may do, or how
// its approvals and secrets work: an approval bound to a manifest stops
// counting when any of them that swim.yml sets changes. To add one, add it
// here and to policyValues; never bump the hash text (docs/formats.md).
var PolicyKeys = []string{"blocked_commands", "runners", "approvals", "signing",
	"secret_env", "secret_env_auto", "secret_env_ignore", "secret_patterns_auto", "env",
	"secrets", "ambient_credentials", "modules"}

// policyValues is each policy key swim.yml can set, with its value.
func (c *Config) policyValues() map[string]any {
	r := c.Repo
	return map[string]any{
		"blocked_commands": r.BlockedCommands, "runners": r.Runners, "approvals": r.Approvals,
		"signing": r.Signing, "secret_env": r.SecretEnv, "secret_env_auto": r.SecretEnvAuto,
		"secret_env_ignore": r.SecretEnvIgnore, "secret_patterns_auto": r.SecretPatternsAuto,
		"env": r.Env, "secrets": r.Secrets, "ambient_credentials": r.AmbientCredentials,
		"modules": r.Modules,
	}
}

// isSet reports whether swim.yml set a policy value: a list or map it
// wrote (even empty), a pointer it filled, a non-empty string.
func isSet(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Invalid:
		return false
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return !rv.IsNil()
	case reflect.String:
		return rv.Len() > 0
	}
	return !rv.IsZero()
}

// PolicySet is the policy keys swim.yml sets, sorted, each with the
// SHA-256 of its value: what PolicyHash covers, key by key, so a stale
// approval can say which key changed.
func (c *Config) PolicySet() map[string]string {
	out := map[string]string{}
	for k, v := range c.policyValues() {
		if isSet(v) {
			out[k] = policyValueHash(k, v)
		}
	}
	return out
}

func policyValueHash(k string, v any) string {
	data, _ := yaml.Marshal(map[string]any{k: v})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PolicyHash is the SHA-256 of the policy keys swim.yml sets, in a fixed
// text form ("swim policy v2", then "<key> <sha256 of its value>" per set
// key, sorted). Only the committed swim.yml counts: an operator's own
// config.yml differs from machine to machine, and an approval signed on one
// must hold on another. Keys outside PolicyKeys (max_parallel, interrupt, ...),
// and policy keys swim.yml doesn't set, never change it.
func (c *Config) PolicyHash() string {
	set := c.PolicySet()
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	text := "swim policy v2\n"
	for _, k := range keys {
		text += k + " " + set[k] + "\n"
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
