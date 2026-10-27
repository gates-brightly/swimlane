package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/cache"
)

// Keys are the settings `swim config <key> [value]` reads and writes.
var Keys = []string{"interrupt", "interrupt_grace", "term_grace", "approvals", "signing", "signing_key", "msg_log_interval", "outputs_keep", "queue_lease", "claim_lease", "pause_warn", "watch_batches", "cache_dir", "cache_max", "cache_slug"}

// Get returns the effective value of one of Keys, spelled as in config.
func (c *Config) Get(key string) (string, error) {
	if ext, ok := RunnerKey(key); ok {
		return c.Runners[ext], nil
	}
	switch key {
	case "interrupt":
		return c.InterruptMode(), nil
	case "approvals":
		return c.ApprovalMode(), nil
	case "signing":
		return c.SigningMode(), nil
	case "signing_key":
		return c.SigningKey, nil
	case "msg_log_interval":
		return c.MsgLogEvery().String(), nil
	case "outputs_keep":
		return c.OutputsKeepFor().String(), nil
	case "queue_lease":
		if d := c.QueueLeaseFor(); d > 0 {
			return d.String(), nil
		}
		return "2m0s", nil
	case "claim_lease":
		return c.ClaimLeaseFor().String(), nil
	case "pause_warn":
		return c.PauseWarnFor().String(), nil
	case "watch_batches":
		return strconv.Itoa(c.WatchBatchesFor()), nil
	case "cache_dir":
		return c.CacheBase()
	case "cache_max":
		if n := c.CacheMaxBytes(); n > 0 {
			return FormatSize(n), nil
		}
		return "0 (no cap)", nil
	case "cache_slug":
		return c.CacheSlugFor()
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
	case "approvals":
		v := strings.ToLower(strings.TrimSpace(value))
		if v != ApprovalsPrompt && v != ApprovalsFlags {
			return nil, fmt.Errorf("approvals must be prompt or flags, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "outputs_keep":
		v := strings.TrimSpace(value)
		if _, ok := parseDays(v); !ok {
			return nil, fmt.Errorf("outputs_keep must be a duration like 14d or 36h, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "queue_lease":
		v := strings.TrimSpace(value)
		if d, err := time.ParseDuration(v); err != nil || d < 10*time.Second {
			return nil, fmt.Errorf("queue_lease must be a duration of 10s or more, like 2m, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "pause_warn":
		v := strings.TrimSpace(value)
		if d, err := time.ParseDuration(v); err != nil || d < time.Minute {
			return nil, fmt.Errorf("pause_warn must be a duration of 1m or more, like 4h, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "claim_lease":
		v := strings.TrimSpace(value)
		if d, err := time.ParseDuration(v); err != nil || d < time.Minute {
			return nil, fmt.Errorf("claim_lease must be a duration of 1m or more, like 2h, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "cache_dir":
		v := strings.TrimSpace(value)
		if !filepath.IsAbs(v) && v != "~" && !strings.HasPrefix(v, "~/") {
			return nil, fmt.Errorf("cache_dir must be an absolute path (or ~/...), got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "cache_max":
		v := strings.TrimSpace(value)
		if _, err := ParseSize(v); err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "cache_slug":
		v := strings.TrimSpace(value)
		if _, err := cache.Slug("", v); err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "watch_batches":
		v := strings.TrimSpace(value)
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			return nil, fmt.Errorf("watch_batches must be a number, 1 or more, got %q", value)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: v}, nil
	case "interrupt_grace", "term_grace", "msg_log_interval":
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
