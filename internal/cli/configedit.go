package cli

import (
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

const (
	configEditHints = "↑↓ move · Enter ←→ change · d default · q quit"
	configEditKeys  = "dqhl " + string(display.PickEnter) + string(display.PickEsc) + string(display.PickLeft) + string(display.PickRight)
)

// cmdConfigEdit is swim config on a terminal: every setting on one
// screen, swim watch's picker, with what it is now, its default and what
// it takes, to change in place. Enter, Space, ← and → step a key with
// choices; Enter on any other key types its value on the bottom line,
// checked before it's written. It edits `defaults:` in the config file,
// and writes only what differs from swim's default: a value set back to
// the default leaves the file. The first write copies the file as it was
// to config.yml-N.bak (config.Backup).
func cmdConfigEdit() error {
	if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
		return errors.New("swim config edit needs a terminal; swim config KEY VALUE sets one key")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, path := config.RepoRoot(cwd), config.Path()
	p := &display.Picker{In: os.Stdin, Out: os.Stdout, Color: ui.ColorEnabled(os.Stdout), HintsBelow: true, Stay: true}
	defer p.Leave()
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
		typing := p.Field != nil
		res, err := p.Pick("swim config · "+path, configEditHints, configEditKeys, load, time.Hour, nil)
		if err != nil {
			return err
		}
		if res.Quit {
			return nil
		}
		key := res.ID
		if typing {
			// Esc, or Enter with nothing typed, keeps the value.
			if v := strings.TrimSpace(res.Text); res.Key == display.PickEnter && v != "" {
				p.Note = configEditSet(path, root, key, v, backup)
			}
			continue
		}
		if res.Key == 'q' || res.Key == display.PickEsc {
			return nil
		}
		cur, err := config.DefaultsIn(path)
		if err != nil {
			return err
		}
		now, _ := cur.Get(key)
		cs := config.Choices(key)
		step := 0
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
		case display.PickEnter, ' ', 'l', display.PickRight:
			step = 1
		case 'h', display.PickLeft:
			step = -1
		}
		switch {
		case step == 0:
		case cs != nil:
			i := (slices.Index(cs, now) + step + len(cs)) % len(cs)
			p.Note = configEditSet(path, root, key, cs[i], backup)
		case res.Key == display.PickEnter:
			text := configShown(key, now)
			if now == "" {
				text = ""
			}
			p.Field = &display.PickField{Prompt: key + ": ", Text: text, Hint: config.Hint(key) + " · Enter saves · Esc cancels"}
		default:
			p.Note = key + " takes a value: Enter to type it"
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
// swim's default), each with swim's default, what it takes, and what this
// repo's section or swim.yml sets instead. cache_slug isn't one: it names
// one repo's caches.
func configEditRows(path, root string) []display.PickRow {
	cur, err := config.DefaultsIn(path)
	if err != nil {
		return []display.PickRow{{Text: err.Error(), Color: ui.Red}}
	}
	here, _ := config.Load(root)
	type setting struct{ key, value, def, repo string }
	var all []setting
	width := len("default")
	for _, key := range config.Keys {
		if key == "cache_slug" {
			continue
		}
		v, _ := cur.Get(key)
		def, _ := (&config.Config{}).Get(key)
		st := setting{key: key, value: configShown(key, v), def: configShown(key, def)}
		if here != nil {
			if hv, err := here.Get(key); err == nil && hv != v {
				st.repo = configShown(key, hv)
			}
		}
		width = max(width, min(len(st.value), 16), min(len(st.def), 16)) // a longer path runs on
		all = append(all, st)
	}
	line := func(key, value, def, hint string) string {
		return fmt.Sprintf("%-17s %-*s  %-*s  %s", key, width, value, width, def, hint)
	}
	rows := []display.PickRow{
		{Rule: true, Text: "defaults", Right: "only what isn't swim's default is written"},
		{Text: line("", "value", "default", "takes"), Color: ui.Dim},
	}
	for _, st := range all {
		color := ui.Bold
		if st.value == st.def {
			color = ui.Dim
		}
		text := line(st.key, st.value, st.def, config.Hint(st.key))
		if st.repo != "" {
			text += "  · this repo uses " + st.repo
		}
		rows = append(rows, display.PickRow{ID: st.key, Text: text, Color: color})
	}
	return rows
}

// configShown is a value as swim config shows it, and as its field starts
// out (config takes it back): durations without their zero tails (2h, not
// 2h0m0s; outputs_keep in days, 14d), paths in the home directory from ~,
// and "—" for none.
func configShown(key, v string) string {
	if v == "" {
		return "—"
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		if key == "outputs_keep" && d%(24*time.Hour) == 0 {
			return fmt.Sprintf("%dd", d/(24*time.Hour))
		}
		v = d.String()
		if strings.HasSuffix(v, "m0s") { // 2m0s: 2m
			v = v[:len(v)-2]
		}
		if strings.HasSuffix(v, "h0m") { // 2h0m: 2h
			v = v[:len(v)-2]
		}
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "/" && strings.HasPrefix(v, home+"/") {
		return "~" + v[len(home):]
	}
	return v
}
