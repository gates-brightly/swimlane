package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Chime says when swim run / swim all chime as they finish.
type Chime string

const (
	ChimeOn      Chime = "on"      // every finish (YAML true)
	ChimeOff     Chime = "off"     // never (YAML false, the default)
	ChimeFailure Chime = "failure" // only when the run didn't fully pass
)

// Chime styles.
const (
	StyleBell   = "bell"
	StyleSound  = "sound"
	StyleNotify = "notify"
)

// Chime defaults, used when neither the defaults nor the repo set them.
const (
	DefaultChime      = ChimeOff
	DefaultChimeStyle = StyleBell
	DefaultChimeMinS  = 10
)

// ChimeStyles lists the valid chime_style values.
var ChimeStyles = []string{StyleBell, StyleSound, StyleNotify}

// Keys are the settings `swim config <key> [value]` reads and writes.
var Keys = []string{"lanes", "chime", "chime_style", "chime_min_s"}

// ParseChime reads true|false|failure (on|off also accepted).
func ParseChime(s string) (Chime, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "on":
		return ChimeOn, nil
	case "false", "off":
		return ChimeOff, nil
	case "failure":
		return ChimeFailure, nil
	}
	return "", fmt.Errorf("chime must be true, false or failure, got %q", s)
}

// UnmarshalYAML accepts a YAML boolean or the string "failure".
func (c *Chime) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: chime must be true, false or failure", n.Line)
	}
	v, err := ParseChime(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*c = v
	return nil
}

// MarshalYAML writes the value as it is spelled in config: true, false or failure.
func (c Chime) MarshalYAML() (any, error) {
	switch c {
	case ChimeOn:
		return true, nil
	case ChimeOff:
		return false, nil
	}
	return string(c), nil
}

// String is the config spelling: true, false or failure.
func (c Chime) String() string {
	switch c {
	case ChimeOn:
		return "true"
	case ChimeOff:
		return "false"
	}
	return string(c)
}

func (c *Config) fillChimeDefaults() {
	if c.Chime == nil {
		v := DefaultChime
		c.Chime = &v
	}
	if c.ChimeStyle == nil {
		v := DefaultChimeStyle
		c.ChimeStyle = &v
	}
	if c.ChimeMinS == nil {
		v := DefaultChimeMinS
		c.ChimeMinS = &v
	}
}

func (c *Config) validateChime() error {
	if c.ChimeStyle != nil {
		if err := checkStyle(*c.ChimeStyle); err != nil {
			return err
		}
	}
	if c.ChimeMinS != nil && *c.ChimeMinS < 0 {
		return fmt.Errorf("chime_min_s must be 0 or more seconds, got %d", *c.ChimeMinS)
	}
	return nil
}

func checkStyle(s string) error {
	for _, v := range ChimeStyles {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("chime_style must be %s, got %q", strings.Join(ChimeStyles, ", "), s)
}

// ChimeSettings returns the effective chime mode, style and minimum run
// length in seconds, with built-in defaults for anything unset.
func (c *Config) ChimeSettings() (mode Chime, style string, minS int) {
	mode, style, minS = DefaultChime, DefaultChimeStyle, DefaultChimeMinS
	if c.Chime != nil {
		mode = *c.Chime
	}
	if c.ChimeStyle != nil {
		style = *c.ChimeStyle
	}
	if c.ChimeMinS != nil {
		minS = *c.ChimeMinS
	}
	return mode, style, minS
}

// Get returns the effective value of one of Keys, spelled as in config.
func (c *Config) Get(key string) (string, error) {
	mode, style, minS := c.ChimeSettings()
	switch key {
	case "lanes":
		return strconv.Itoa(c.Lanes), nil
	case "chime":
		return mode.String(), nil
	case "chime_style":
		return style, nil
	case "chime_min_s":
		return strconv.Itoa(minS), nil
	}
	return "", unknownKey(key)
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown config key %q; keys: %s", key, strings.Join(Keys, ", "))
}

// keyValue validates value for key and returns the YAML scalar to write.
func keyValue(key, value string) (*yaml.Node, error) {
	intNode := func(n int) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
	}
	switch key {
	case "lanes":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 || n > 99 {
			if err != nil {
				return nil, fmt.Errorf("lanes must be a number 1..99, got %q", value)
			}
			return nil, fmt.Errorf("lanes must be 1..99, got %d", n)
		}
		return intNode(n), nil
	case "chime":
		c, err := ParseChime(value)
		if err != nil {
			return nil, err
		}
		if c == ChimeFailure {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(c)}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: c.String()}, nil
	case "chime_style":
		v := strings.ToLower(strings.TrimSpace(value))
		if err := checkStyle(v); err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case "chime_min_s":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("chime_min_s must be 0 or more seconds, got %q", value)
		}
		return intNode(n), nil
	}
	return nil, unknownKey(key)
}

// Normalize validates value for key and returns it as config spells it
// (chime on -> true).
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

	if v := mapGet(section, key); v != nil {
		// Replace the value in place so comments on it survive.
		v.Kind, v.Tag, v.Value, v.Style, v.Content, v.Alias = val.Kind, val.Tag, val.Value, 0, nil, nil
	} else if key == "lanes" {
		section.Content = append([]*yaml.Node{scalar(key), val}, section.Content...)
	} else {
		section.Content = append(section.Content, scalar(key), val)
	}
	return writeNode(path, &doc)
}
