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
	configEditHints = "↑↓ move · Enter ←→ change · d default · t test notify · q quit"
	configEditKeys  = "dqhlt " + string(display.PickEnter) + string(display.PickEsc) + string(display.PickLeft) + string(display.PickRight)
)

// cmdConfigEdit is swim config on a terminal: every setting on one
// screen, swim watch's picker, with its value at this layer, the layer it
// comes from, its default and what it takes, to change in place. Enter,
// Space, ← and → step a key with choices; Enter on any other key types its
// value on the bottom line, checked before it's written. It edits the
// layer's section of the config file (defaults:, or with --repo / --branch
// repos.NAME / repos.NAME/BRANCH), and writes only what differs from what
// the section would inherit: a value set back to that leaves the file. The
// first write copies the file as it was to config.yml-N.bak
// (config.Backup).
func cmdConfigEdit(layer string) error {
	if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
		return errors.New("swim config edit needs a terminal; swim config KEY VALUE sets one key")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := config.RepoRoot(cwd)
	cfg, err := config.Read(root)
	if err != nil {
		return err
	}
	section, err := cfg.Section(layer)
	if err != nil {
		return err
	}
	path, title := cfg.Path, "defaults"
	if section != "" {
		title = "repos." + section
	}
	p := &display.Picker{In: os.Stdin, Out: os.Stdout, Color: ui.ColorEnabled(os.Stdout), HintsBelow: true, Stay: true}
	defer p.Leave()
	load := func() []display.PickRow { return configEditRows(root, layer) }
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
		res, err := p.Pick("swim config · "+title+" · "+path, configEditHints, configEditKeys, load, time.Hour, nil)
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
				p.Note = configEditSet(root, layer, section, key, v, backup)
			}
			continue
		}
		if res.Key == 'q' || res.Key == display.PickEsc {
			return nil
		}
		if res.Key == 't' {
			// The notification as swim run here would send it now.
			cfg, err := config.Load(root)
			if err != nil {
				p.Note = err.Error()
				continue
			}
			p.Note = testNotify(cfg, root)
			continue
		}
		cur, err := config.Read(root)
		if err != nil {
			return err
		}
		now, _ := cur.Upto(layer).Get(key)
		cs := config.Choices(key)
		step := 0
		switch res.Key {
		case 'd':
			was, err := backup()
			if err != nil {
				p.Note = "not written, no backup: " + err.Error()
				continue
			}
			if err := config.UnsetKey(path, section, key); err != nil {
				return err
			}
			v, _ := cur.Below(layer).Get(key)
			p.Note = fmt.Sprintf("%s: back to %s%s", key, inheritedFrom(cur, layer, key, configShown(key, v)), was)
		case display.PickEnter, ' ', 'l', display.PickRight:
			step = 1
		case 'h', display.PickLeft:
			step = -1
		}
		switch {
		case step == 0:
		case cs != nil:
			i := (slices.Index(cs, now) + step + len(cs)) % len(cs)
			p.Note = configEditSet(root, layer, section, key, cs[i], backup)
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

// configEditSet writes key: value to the layer's section, or removes the
// key when value is what the section would inherit anyway, and says what
// it did. A valid value is backed up first; one that isn't writes nothing.
func configEditSet(root, layer, section, key, value string, backup func() (string, error)) string {
	cfg, err := config.Read(root)
	if err != nil {
		return err.Error()
	}
	same, err := cfg.Inherited(layer, key, value)
	if err != nil {
		return err.Error()
	}
	was, err := backup()
	if err != nil {
		return "not written, no backup: " + err.Error()
	}
	if same {
		if err := config.UnsetKey(cfg.Path, section, key); err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%s: %s, %s (not written)%s", key, value, inheritedFrom(cfg, layer, key, ""), was)
	}
	if err := config.SetKey(cfg.Path, section, key, value); err != nil {
		return err.Error()
	}
	v, _ := config.Normalize(key, value)
	return fmt.Sprintf("%s: %s (written)%s", key, v, was)
}

// inheritedFrom says where the value a layer inherits for key comes from:
// "the default, 5s" or "20s, as defaults sets it" (value omitted when "").
func inheritedFrom(cfg *config.Config, layer, key, value string) string {
	from := cfg.Below(layer).From(key)
	if from == config.LayerDefault {
		if value == "" {
			return "the default"
		}
		return "the default, " + value
	}
	if value == "" {
		return "as " + from + " sets it"
	}
	return value + ", as " + from + " sets it"
}

// configEditRows are the settings as the layer and those below it give
// them, each with the layer it comes from, swim's default, what it takes,
// and what this branch uses instead when a layer above wins.
func configEditRows(root, layer string) []display.PickRow {
	all, err := config.Read(root)
	if err != nil {
		return []display.PickRow{{Text: err.Error(), Color: ui.Red}}
	}
	here := all.Upto(layer)
	type setting struct{ key, value, from, def, over string }
	var rows []setting
	width := len("default")
	for _, key := range config.Keys {
		v, _ := here.Get(key)
		def, _ := (&config.Config{}).Get(key)
		st := setting{key: key, value: configShown(key, v), from: here.From(key), def: configShown(key, def)}
		if av, err := all.Get(key); err == nil && av != v {
			st.over = configShown(key, av) + " (" + all.From(key) + ")"
		}
		width = max(width, min(len(st.value), 16), min(len(st.def), 16)) // a longer path runs on
		rows = append(rows, st)
	}
	line := func(key, value, from, def, hint string) string {
		return fmt.Sprintf("%-17s %-*s  %-11s  %-*s  %s", key, width, value, from, width, def, hint)
	}
	right := "only what differs from the layers below is written"
	out := []display.PickRow{
		{Rule: true, Text: layer, Right: right},
		{Text: line("", "value", "from", "default", "takes"), Color: ui.Dim},
	}
	for _, st := range rows {
		color := ui.Bold
		if st.from != layer {
			color = ui.Dim
		}
		text := line(st.key, st.value, st.from, st.def, config.Hint(st.key))
		if st.over != "" {
			text += "  · here it's " + st.over
		}
		out = append(out, display.PickRow{ID: st.key, Text: text, Color: color})
		if st.key == "notify_command" {
			// How the last notification went: is the endpoint working?
			if sum := notifySummary(all, time.Now()); sum != "" {
				c := ui.Dim
				if strings.HasPrefix(sum, "last send failed") {
					c = ui.Red
				}
				out = append(out, display.PickRow{Text: fmt.Sprintf("%-17s %s · t sends a test", "", sum), Color: c})
			}
		}
	}
	return out
}

// configShown is a value as swim config shows it, and as its field starts
// out (config takes it back): durations without their zero tails (2h, not
// 2h0m0s), paths in the home directory from ~,
// and "—" for none.
func configShown(key, v string) string {
	if v == "" {
		return "—"
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
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
