package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/ui"
)

const configEditHints = "↑↓ move · Enter change · d default · q quit"

// cmdConfigEdit is swim config edit: every setting on one screen, with
// what it is now, to change in place. It edits `defaults:` in the config
// file, and writes only what differs from swim's default: a value set
// back to the default leaves the file. The first write copies the file
// as it was to config.yml-N.bak (config.Backup).
func cmdConfigEdit() error {
	if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
		return errors.New("swim config edit needs a terminal; swim config KEY VALUE sets one key")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, path := config.RepoRoot(cwd), config.Path()
	p := &display.Picker{In: os.Stdin, Out: os.Stdout, Color: ui.ColorEnabled(os.Stdout), HintsBelow: true}
	in := bufio.NewReader(os.Stdin)
	load := func() []display.PickRow { return configEditRows(path, root) }
	// backup copies the file before the session's first write, and says
	// where to, once.
	backedUp := false
	backup := func() (string, error) {
		if backedUp {
			return "", nil
		}
		bak, err := config.Backup(path)
		if err != nil {
			return "", err
		}
		backedUp = true
		if bak == "" {
			return "", nil
		}
		return " · was " + bak, nil
	}
	for {
		res, err := p.Pick("swim config · "+path, configEditHints, "dq"+string(display.PickEnter)+string(display.PickEsc), load, time.Hour, nil)
		if err != nil {
			return err
		}
		if res.Quit || res.Key == 'q' || res.Key == display.PickEsc {
			return nil
		}
		key := res.ID
		cur, err := config.DefaultsIn(path)
		if err != nil {
			return err
		}
		now, _ := cur.Get(key)
		switch res.Key {
		case 'd':
			was, err := backup()
			if err != nil {
				p.Note = "not written, no backup: " + err.Error()
				continue
			}
			if err := config.UnsetKey(path, key); err != nil {
				return err
			}
			def, _ := (&config.Config{}).Get(key)
			p.Note = fmt.Sprintf("%s: back to the default, %s%s", key, def, was)
		case display.PickEnter:
			value := ""
			if cs := config.Choices(key); cs != nil {
				value = cs[(slices.Index(cs, now)+1)%len(cs)]
			} else {
				fmt.Printf("%s (now %s; empty keeps it): ", key, now)
				line, _ := in.ReadString('\n')
				if value = strings.TrimSpace(line); value == "" {
					continue
				}
			}
			p.Note = configEditSet(path, root, key, value, backup)
		}
	}
}

// configEditSet writes key: value under defaults, or removes the key when
// value is swim's default, and says what it did. A valid value is backed
// up first; one that isn't writes nothing.
func configEditSet(path, root, key, value string, backup func() (string, error)) string {
	def, err := config.IsDefault(key, value)
	if err != nil {
		return err.Error()
	}
	was, err := backup()
	if err != nil {
		return "not written, no backup: " + err.Error()
	}
	if def {
		if err := config.UnsetKey(path, key); err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%s: %s, the default (not written)%s", key, value, was)
	}
	if err := config.SetKey(path, root, key, value, false); err != nil {
		return err.Error()
	}
	v, _ := config.Normalize(key, value)
	return fmt.Sprintf("%s: %s (written)%s", key, v, was)
}

// configEditRows are the settings, as the defaults section sets them (or
// swim's default), with what this repo's section or swim.yml sets instead.
// cache_slug isn't one: it names one repo's caches.
func configEditRows(path, root string) []display.PickRow {
	cur, err := config.DefaultsIn(path)
	if err != nil {
		return []display.PickRow{{Text: err.Error(), Color: ui.Red}}
	}
	here, _ := config.Load(root)
	rows := []display.PickRow{{Rule: true, Text: "defaults", Right: "only what isn't swim's default is written"}}
	for _, key := range config.Keys {
		if key == "cache_slug" {
			continue
		}
		v, _ := cur.Get(key)
		def, _ := (&config.Config{}).Get(key)
		shown := v
		if shown == "" {
			shown = "(none)"
		}
		text := fmt.Sprintf("%-18s %s", key, shown)
		color := ui.Bold
		if v == def {
			text += "  (default)"
			color = ui.Dim
		}
		if here != nil {
			if hv, err := here.Get(key); err == nil && hv != v {
				text += "  · this repo uses " + hv
			}
		}
		rows = append(rows, display.PickRow{ID: key, Text: text, Color: color})
	}
	return rows
}
