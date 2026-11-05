package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Keys are the settings `swim config <key> [value]` reads and writes.
var Keys = []string{"interrupt", "interrupt_grace", "term_grace", "signing", "signing_key", "queue_lease", "claim_lease", "watch_batches", "notify", "notify_url", "notify_command"}

// Get returns the effective value of one of Keys, spelled as in config.
func (c *Config) Get(key string) (string, error) {
	if ext, ok := RunnerKey(key); ok {
		return c.Runners[ext], nil
	}
	switch key {
	case "interrupt":
		return c.InterruptMode(), nil
	case "signing":
		return c.SigningMode(), nil
	case "signing_key":
		return c.SigningKey, nil
	case "queue_lease":
		if d := c.QueueLeaseFor(); d > 0 {
			return d.String(), nil
		}
		return "2m0s", nil
	case "claim_lease":
		return c.ClaimLeaseFor().String(), nil
	case "watch_batches":
		return strconv.Itoa(c.WatchBatchesFor()), nil
	case "notify":
		return strconv.FormatBool(c.NotifyOn()), nil
	case "notify_url":
		return c.NotifyURL, nil
	case "notify_command":
		return c.NotifyCommand, nil
	case "interrupt_grace", "term_grace":
		ig, tg := c.Graces()
		if key == "term_grace" {
			return tg.String(), nil
		}
		return ig.String(), nil
	}
	return "", unknownKey(key)
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown config key %q; keys: %s, runners.EXT", key, strings.Join(Keys, ", "))
}

// RunnerKey reports whether key is runners.EXT (the runner for lane
// scripts ending in .EXT) and returns EXT.
func RunnerKey(key string) (string, bool) {
	ext, ok := strings.CutPrefix(key, "runners.")
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	if !ok || ext == "" || strings.ContainsAny(ext, " ./") {
		return "", false
	}
	return ext, true
}

// keyValue validates value for key and returns the YAML scalar to write.
func keyValue(key, value string) (*yaml.Node, error) {
	if _, ok := RunnerKey(key); ok {
		v := strings.TrimSpace(value)
		if v == "" {
			return nil, fmt.Errorf("%s needs a command, e.g. tsx or /usr/bin/python3", key)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	}
	switch key {
	case "interrupt":
		v := strings.ToLower(strings.TrimSpace(value))
		if v != InterruptGraceful && v != InterruptImmediate {
			return nil, fmt.Errorf("interrupt must be graceful or immediate, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "signing":
		v := strings.ToLower(strings.TrimSpace(value))
		if v != SigningAuto && v != SigningRequired && v != SigningOff {
			return nil, fmt.Errorf("signing must be auto, required or off, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "signing_key":
		v := strings.TrimSpace(value)
		if v == "" {
			return nil, fmt.Errorf("signing_key needs a path to an SSH key (or its .pub, for ssh-agent)")
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "queue_lease":
		v := strings.TrimSpace(value)
		if d, err := time.ParseDuration(v); err != nil || d < 10*time.Second {
			return nil, fmt.Errorf("queue_lease must be a duration of 10s or more, like 2m, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "claim_lease":
		v := strings.TrimSpace(value)
		if d, err := time.ParseDuration(v); err != nil || d < time.Minute {
			return nil, fmt.Errorf("claim_lease must be a duration of 1m or more, like 2h, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "watch_batches":
		v := strings.TrimSpace(value)
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			return nil, fmt.Errorf("watch_batches must be a number, 1 or more, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: v}, nil
	case "notify":
		v, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("notify must be true or false, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}, nil
	case "notify_url":
		v := strings.TrimSpace(value)
		if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("notify_url must be an http(s) URL, e.g. https://ntfy.sh/TOPIC, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "notify_command":
		v := strings.TrimSpace(value)
		if v == "" {
			return nil, fmt.Errorf(`notify_command needs a command, e.g. curl -s -H "Title: $SWIM_TITLE" -d "$SWIM_BODY" https://ntfy.sh/TOPIC`)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "interrupt_grace", "term_grace":
		v := strings.TrimSpace(value)
		if d, err := time.ParseDuration(v); err != nil || d < 0 {
			return nil, fmt.Errorf("%s must be a duration like 5s or 1m, got %q", key, value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	}
	return nil, unknownKey(key)
}

// Normalize validates value for key and returns it as config spells it
// (approvals Prompt -> prompt).
func Normalize(key, value string) (string, error) {
	n, err := keyValue(key, value)
	if err != nil {
		return "", err
	}
	return n.Value, nil
}

// SetKey sets one of Keys under `defaults:`, or with repo under root's repo
// section (created if needed), keeping the rest of the file and its
// comments. The file is created if missing. The value is validated first.
func SetKey(path, root, key, value string, repo bool) error {
	val, err := keyValue(key, value)
	if err != nil {
		return err
	}
	if repo {
		if _, _, err := EnsureRepo(path, root); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte(defaultFile)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level must be a mapping", path)
	}

	var section *yaml.Node
	if repo {
		repos := mapGet(top, "repos")
		if repos == nil || repos.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: no repos mapping", path)
		}
		for i := 0; i+1 < len(repos.Content); i += 2 {
			if sameRepo(repos.Content[i].Value, root) {
				section = repos.Content[i+1]
				break
			}
		}
		if section == nil {
			return fmt.Errorf("%s: no section for %s", path, root)
		}
	} else {
		section = mapGet(top, "defaults")
		if section == nil {
			top.Content = append([]*yaml.Node{scalar("defaults"), {Kind: yaml.MappingNode}}, top.Content...)
			section = top.Content[1]
		}
	}
	if section.Kind != yaml.MappingNode {
		*section = yaml.Node{Kind: yaml.MappingNode}
	}
	if len(section.Content) == 0 {
		section.Style = 0 // render block style even if it was `{}`
	}

	if ext, ok := RunnerKey(key); ok {
		// runners.EXT is a key of the runners mapping.
		runners := mapGet(section, "runners")
		if runners == nil {
			section.Content = append(section.Content, scalar("runners"), &yaml.Node{Kind: yaml.MappingNode})
			runners = section.Content[len(section.Content)-1]
		}
		if runners.Kind != yaml.MappingNode {
			*runners = yaml.Node{Kind: yaml.MappingNode}
		}
		runners.Style = 0
		section, key = runners, ext
	}
	if v := mapGet(section, key); v != nil {
		// Replace the value in place so comments on it survive.
		v.Kind, v.Tag, v.Value, v.Style, v.Content, v.Alias = val.Kind, val.Tag, val.Value, 0, nil, nil
	} else {
		section.Content = append(section.Content, scalar(key), val)
	}
	return writeNode(path, &doc)
}

// Choices are the values one of Keys takes, when it takes a fixed few (""
// otherwise): what swim config edit steps through.
func Choices(key string) []string {
	switch key {
	case "interrupt":
		return []string{InterruptGraceful, InterruptImmediate}
	case "signing":
		return []string{SigningAuto, SigningRequired, SigningOff}
	case "notify":
		return []string{"false", "true"}
	}
	return nil
}

// IsDefault reports whether value for key is what swim uses with the key
// unset (10s for term_grace, say). The value is
// validated first.
func IsDefault(key, value string) (bool, error) {
	val, err := keyValue(key, value)
	if err != nil {
		return false, err
	}
	var c Config
	m := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(key), val}}
	if err := m.Decode(&c.Settings); err != nil {
		return false, err
	}
	got, err := c.Get(key)
	if err != nil {
		return false, err
	}
	def, err := (&Config{}).Get(key)
	return err == nil && got == def, nil
}

// UnsetKey removes one of Keys from `defaults:`, keeping the rest of the
// file and its comments, so swim goes back to its default (or what a repo
// sets). A missing file or key is no change.
func UnsetKey(path, key string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	section := mapGet(doc.Content[0], "defaults")
	if section == nil || section.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(section.Content); i += 2 {
		if section.Content[i].Value == key {
			section.Content = slices.Delete(section.Content, i, i+2)
			return writeNode(path, &doc)
		}
	}
	return nil
}

// DefaultsIn is what the `defaults:` section of the config file at path
// sets, as a Config whose Get says what it means (swim's own default for
// a key it leaves unset).
func DefaultsIn(path string) (*Config, error) {
	f, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return &Config{Path: path, Settings: f.Defaults}, nil
}

// Backup copies the config file at path to path-N.bak, N one past the
// highest copy there, the way Nix numbers a profile's generations
// (profile-41-link, profile-42-link), and returns the copy's path. A
// missing file, or one the newest copy already holds, isn't copied: ""
// and no error.
func Backup(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	dir, base := filepath.Split(path)
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	last := 0
	for _, e := range entries {
		mid, ok := strings.CutPrefix(e.Name(), base+"-")
		if !ok {
			continue
		}
		if mid, ok = strings.CutSuffix(mid, ".bak"); !ok {
			continue
		}
		if n, err := strconv.Atoi(mid); err == nil && n > last {
			last = n
		}
	}
	if last > 0 {
		if prev, err := os.ReadFile(fmt.Sprintf("%s-%d.bak", path, last)); err == nil && bytes.Equal(prev, data) {
			return "", nil
		}
	}
	bak := fmt.Sprintf("%s-%d.bak", path, last+1)
	return bak, os.WriteFile(bak, data, info.Mode().Perm())
}

// Hint says what one of Keys takes, as keyValue checks it: its choices,
// or the kind of value, briefly.
func Hint(key string) string {
	if cs := Choices(key); cs != nil {
		return strings.Join(cs, " | ")
	}
	switch key {
	case "interrupt_grace", "term_grace":
		return "duration: 5s, 1m"
	case "queue_lease":
		return "duration, 10s or more"
	case "claim_lease":
		return "duration, 1m or more"
	case "watch_batches":
		return "number, 1 or more"
	case "signing_key":
		return "SSH key path (.pub for ssh-agent)"
	case "notify_url":
		return "URL: https://ntfy.sh/TOPIC"
	case "notify_command":
		return "shell command (instead of notify_url)"
	}
	return ""
}
