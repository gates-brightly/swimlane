package chime

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
)

// The full decision table: mode × result × duration × terminal × CI × style.
func TestShouldDecisionTable(t *testing.T) {
	modes := []config.Chime{config.ChimeOn, config.ChimeOff, config.ChimeFailure}
	results := []Result{Passed, Failed, Interrupted}
	const minS = 10
	durations := map[string]time.Duration{"below": 9 * time.Second, "at": 10 * time.Second, "above": time.Minute}
	count := 0
	for _, mode := range modes {
		for _, res := range results {
			for dname, d := range durations {
				for _, tty := range []bool{true, false} {
					for _, ci := range []bool{true, false} {
						for _, style := range config.ChimeStyles {
							in := Input{Mode: mode, Style: style, MinS: minS, Result: res, Elapsed: d, TTY: tty, CI: ci}
							want := !ci &&
								(mode == config.ChimeOn || (mode == config.ChimeFailure && res != Passed)) &&
								dname != "below" &&
								(tty || style == config.StyleNotify)
							if got := Should(in); got != want {
								t.Errorf("Should(%+v) = %v, want %v", in, got, want)
							}
							count++
						}
					}
				}
			}
		}
	}
	if count != 3*3*3*2*2*3 {
		t.Fatalf("table has %d rows", count)
	}
}

func TestShouldSpotChecks(t *testing.T) {
	base := Input{Mode: config.ChimeOn, Style: config.StyleBell, MinS: 10, Result: Passed, Elapsed: 42 * time.Second, TTY: true}
	cases := []struct {
		name string
		edit func(*Input)
		want bool
	}{
		{"on, long, tty", func(*Input) {}, true},
		{"off", func(i *Input) { i.Mode = config.ChimeOff }, false},
		{"unset mode", func(i *Input) { i.Mode = "" }, false},
		{"failure + pass", func(i *Input) { i.Mode = config.ChimeFailure }, false},
		{"failure + fail", func(i *Input) { i.Mode, i.Result = config.ChimeFailure, Failed }, true},
		{"failure + interrupt", func(i *Input) { i.Mode, i.Result = config.ChimeFailure, Interrupted }, true},
		{"short run", func(i *Input) { i.Elapsed = 3 * time.Second }, false},
		{"min 0, instant run", func(i *Input) { i.MinS, i.Elapsed = 0, 0 }, true},
		{"piped", func(i *Input) { i.TTY = false }, false},
		{"piped notify", func(i *Input) { i.TTY, i.Style = false, config.StyleNotify }, true},
		{"piped sound", func(i *Input) { i.TTY, i.Style = false, config.StyleSound }, false},
		{"CI", func(i *Input) { i.CI = true }, false},
		{"CI notify", func(i *Input) { i.CI, i.Style = true, config.StyleNotify }, false},
	}
	for _, c := range cases {
		in := base
		c.edit(&in)
		if got := Should(in); got != c.want {
			t.Errorf("%s: Should = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsCI(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "TRUE": true, "1": true, "": false, "false": false, "0": false} {
		if got := IsCI(func(string) string { return v }); got != want {
			t.Errorf("CI=%q: %v", v, got)
		}
	}
}

type fake struct {
	out     bytes.Buffer
	started [][]string
	have    map[string]bool
}

func (f *fake) chimer(goos string, tty bool) Chimer {
	return Chimer{
		Out: &f.out, TTY: tty, GOOS: goos,
		LookPath: func(name string) (string, error) {
			if f.have[name] {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Start: func(name string, args ...string) error {
			f.started = append(f.started, append([]string{name}, args...))
			return errors.New("ignored")
		},
	}
}

func TestRingBell(t *testing.T) {
	f := &fake{}
	f.chimer("linux", true).Ring(config.StyleBell, false, "m")
	if f.out.String() != "\a" || len(f.started) != 0 {
		t.Fatalf("pass: out=%q started=%v", f.out.String(), f.started)
	}
	f = &fake{}
	f.chimer("linux", true).Ring(config.StyleBell, true, "m")
	if f.out.String() != "\a\a" {
		t.Fatalf("fail: out=%q", f.out.String())
	}
	// No bell into a pipe (notify chimes without a terminal).
	f = &fake{have: map[string]bool{"notify-send": true}}
	f.chimer("linux", false).Ring(config.StyleNotify, false, "1 passed (0:42)")
	if f.out.Len() != 0 || fmt.Sprint(f.started) != "[[notify-send swim 1 passed (0:42)]]" {
		t.Fatalf("piped notify: out=%q started=%v", f.out.String(), f.started)
	}
}

func TestRingCommands(t *testing.T) {
	all := map[string]bool{"afplay": true, "osascript": true, "canberra-gtk-play": true, "paplay": true, "notify-send": true}
	cases := []struct {
		goos, style string
		failed      bool
		have        map[string]bool
		want        string // joined command, "" for bell only
	}{
		{"darwin", config.StyleSound, false, all, "afplay /System/Library/Sounds/Glass.aiff"},
		{"darwin", config.StyleSound, true, all, "afplay /System/Library/Sounds/Basso.aiff"},
		{"darwin", config.StyleNotify, false, all, `osascript -e display notification "say \"hi\"" with title "swim"`},
		{"darwin", config.StyleSound, false, nil, ""},
		{"linux", config.StyleSound, false, all, "canberra-gtk-play -i complete"},
		{"linux", config.StyleSound, true, all, "canberra-gtk-play -i dialog-error"},
		{"linux", config.StyleSound, true, map[string]bool{"paplay": true}, "paplay /usr/share/sounds/freedesktop/stereo/dialog-error.oga"},
		{"linux", config.StyleSound, false, nil, ""},
		{"linux", config.StyleNotify, false, all, `notify-send swim say "hi"`},
		{"linux", config.StyleNotify, false, nil, ""},
		{"freebsd", config.StyleSound, false, all, ""},
		{"linux", config.StyleBell, false, all, ""},
	}
	for _, c := range cases {
		f := &fake{have: c.have}
		f.chimer(c.goos, true).Ring(c.style, c.failed, `say "hi"`)
		got := ""
		if len(f.started) > 1 {
			t.Errorf("%+v: started %d commands", c, len(f.started))
		} else if len(f.started) == 1 {
			got = strings.Join(f.started[0], " ")
		}
		if got != c.want {
			t.Errorf("%s %s failed=%v: got %q, want %q", c.goos, c.style, c.failed, got, c.want)
		}
		// The bell always rings on a terminal, whatever else happens.
		wantBell := "\a"
		if c.failed {
			wantBell = "\a\a"
		}
		if f.out.String() != wantBell {
			t.Errorf("%s %s: bell %q", c.goos, c.style, f.out.String())
		}
	}
}

func TestMessage(t *testing.T) {
	if got := Message(97, 2, 0, 0, "0:42"); got != "97 passed, 2 failed (0:42)" {
		t.Errorf("got %q", got)
	}
	if got := Message(1, 0, 2, 1, "1:00"); got != "1 passed, 2 skipped, 1 interrupted (1:00)" {
		t.Errorf("got %q", got)
	}
}

// StartDetached must return at once, even for a command that runs long.
func TestStartDetachedDoesNotWait(t *testing.T) {
	start := time.Now()
	if err := StartDetached("sleep", "5"); err != nil {
		t.Skip("no sleep binary:", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("StartDetached waited %s", took)
	}
	if err := StartDetached("swim-no-such-command-xyz"); err == nil {
		t.Fatal("missing command started")
	}
}
