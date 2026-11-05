package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnknownKey is a key config.yml sets that swim doesn't read: config.yml
// is read loosely, so a typo or a removed setting is otherwise silently
// ignored.
type UnknownKey struct {
	Key     string // as written
	Section string // "defaults", "repos NAME", "repos NAME/BRANCH" or "" (the top level)
	Line    int
}

// settingKeys are the keys a defaults or repos section may set.
var settingKeys = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(Settings{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}()

// UnknownKeys lists the keys the config file at path sets that swim
// doesn't read. A missing file has none.
func UnknownKeys(path string) ([]UnknownKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	var out []UnknownKey
	section := func(name string, m *yaml.Node) {
		if m.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			if k := m.Content[i]; !settingKeys[k.Value] {
				out = append(out, UnknownKey{Key: k.Value, Section: name, Line: k.Line})
			}
		}
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i], top.Content[i+1]
		switch k.Value {
		case "defaults":
			section("defaults", v)
		case "repos":
			if v.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				section("repos "+v.Content[j].Value, v.Content[j+1])
			}
		default:
			out = append(out, UnknownKey{Key: k.Value, Line: k.Line})
		}
	}
	return out, nil
}
