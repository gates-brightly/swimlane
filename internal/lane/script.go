package lane

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// A lane's script is lane.N.sh, or any other runnable: lane.N.py,
// lane.N.js, lane.N.ts, a binary lane.N, ... (exactly one per lane). Its
// metadata is the script's comment header (# or // comments), or a sidecar
// lane.N.yml, whose keys win.

// RunnerExts are the extensions swim knows a runner for (see the runner
// package); a lane.N.<ext> file with another extension is a lane script
// only if it is executable.
var RunnerExts = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "py": true, "js": true, "mjs": true, "cjs": true,
	"ts": true, "mts": true, "rb": true, "pl": true, "php": true, "lua": true, "go": true,
}

// notScripts are extensions of files next to lane scripts that never are one.
var notScripts = map[string]bool{
	"yml": true, "yaml": true, "tmp": true, "md": true, "txt": true, "log": true, "json": true,
	"bak": true, "orig": true, "swp": true, "rc": true, "lock": true, "pid": true, "signature": true,
}

// ParseScriptName reports whether a file name (no directory) names a lane
// script by its form: lane.N or lane.N.<ext> for a runner or unknown
// extension (not lane.N.yml, lane.N.sh.tmp, ...). Without the file at hand
// it can't check the executable bit; Scripts does.
func ParseScriptName(name string) (n int, ext string, ok bool) {
	// lane.N or lane.N.<ext>, ext letters and digits.
	rest, found := strings.CutPrefix(name, "lane.")
	if !found {
		return 0, "", false
	}
	num, ext, _ := strings.Cut(rest, ".")
	if !isDigits(num) || strings.Contains(rest, ".") && (ext == "" || !allOf(ext, func(r rune) bool { return isLetter(r) || isDigit(r) })) {
		return 0, "", false
	}
	if notScripts[strings.ToLower(ext)] {
		return 0, "", false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return 0, "", false
	}
	return n, strings.ToLower(ext), true
}

// ParseLaneFile reports whether name is one of a lane's committed files:
// its script (by name) or its sidecar lane.N.yml.
func ParseLaneFile(name string) (int, bool) {
	if n, _, ok := ParseScriptName(name); ok {
		return n, true
	}
	// lane.N.yml or lane.N.yaml
	if rest, ok := strings.CutPrefix(name, "lane."); ok {
		num, ext, _ := strings.Cut(rest, ".")
		if isDigits(num) && (ext == "yml" || ext == "yaml") {
			n, err := strconv.Atoi(num)
			return n, err == nil && n > 0
		}
	}
	return 0, false
}

// Scripts returns the files in root that are lane n's script, sorted with
// lane.N.sh first. More than one is a conflict (see ScriptConflict).
func Scripts(root string, n int) []string {
	var out []string
	cands, _ := filepath.Glob(filepath.Join(root, fmt.Sprintf("lane.%d", n)))
	more, _ := filepath.Glob(filepath.Join(root, fmt.Sprintf("lane.%d.*", n)))
	for _, p := range append(cands, more...) {
		k, ext, ok := ParseScriptName(filepath.Base(p))
		if !ok || k != n {
			continue
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		if ext != "" && !RunnerExts[ext] && st.Mode()&0o111 == 0 {
			continue // lane.1.cfg, notes: not runnable
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := strings.HasSuffix(out[i], ".sh"), strings.HasSuffix(out[j], ".sh")
		if si != sj {
			return si
		}
		return out[i] < out[j]
	})
	return out
}

// Script is lane n's script: the lane.N[.ext] file on disk (lane.N.sh first
// if there are several), or lane.N.sh when there is none.
func Script(root string, n int) string {
	if s := Scripts(root, n); len(s) > 0 {
		return s[0]
	}
	return filepath.Join(root, fmt.Sprintf("lane.%d.sh", n))
}

// ScriptName is the base name of lane n's script (lane.3.sh, lane.3.py),
// for messages.
func ScriptName(root string, n int) string {
	if s := Scripts(root, n); len(s) > 0 {
		return filepath.Base(s[0])
	}
	return filepath.Base(Script(root, n))
}

// ScriptConflict is an error if lane n has more than one script.
func ScriptConflict(root string, n int) error {
	s := Scripts(root, n)
	if len(s) < 2 {
		return nil
	}
	names := make([]string, len(s))
	for i, p := range s {
		names[i] = filepath.Base(p)
	}
	return fmt.Errorf("swim %d has %d scripts (%s); a lane has exactly one: remove or rename the others", n, len(s), strings.Join(names, ", "))
}

// Sidecar is lane n's metadata file, lane.N.yml (lane.N.yaml if only that
// exists).
func Sidecar(root string, n int) string {
	p := filepath.Join(root, fmt.Sprintf("lane.%d.yml", n))
	if _, err := os.Stat(p); err != nil {
		alt := filepath.Join(root, fmt.Sprintf("lane.%d.yaml", n))
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	return p
}

// IsBinary reports whether data looks like a compiled program rather than a
// text script (a NUL byte in its first 8KB, or an executable magic number).
func IsBinary(data []byte) bool {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, magic := range [][]byte{{0x7f, 'E', 'L', 'F'}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}, {'M', 'Z'}} {
		if bytes.HasPrefix(head, magic) {
			return true
		}
	}
	return bytes.IndexByte(head, 0) >= 0
}

// UsesLib reports whether a script loads swim's bash library (lane_init):
// it then runs as it always has, managing its own round, rather than
// inside swim's wrapper.
func UsesLib(src string) bool {
	for _, l := range strings.Split(src, "\n") {
		if firstWordIs(l, "lane_init") {
			return true
		}
	}
	return false
}

// sideFile is lane.N.yml. Every key is optional; a key that is set wins
// over the script's header.
type sideFile struct {
	Round       *string           `yaml:"round"`
	Job         *string           `yaml:"job"`
	After       *strList          `yaml:"after"`
	Owner       *string           `yaml:"owner"`
	Created     *string           `yaml:"created"`
	Guards      map[string]string `yaml:"guards"`
	Decisions   map[string]string `yaml:"decisions"`
	Timeout     *string           `yaml:"timeout"`
	StepTimeout *string           `yaml:"step-timeout"`
	Locks       *strList          `yaml:"locks"`
	Stub        *bool             `yaml:"stub"`
	// Fail: hard (script syntax 3).
	Fail     *string  `yaml:"fail"`
	Outputs  *strList `yaml:"outputs"`
	Includes *strList `yaml:"includes"`
	Observe  []string `yaml:"observe"`
	Cache    *strList `yaml:"cache"`
	Modules  *strList `yaml:"modules"`
}

// strList is a YAML list, or one scalar of space/comma separated words.
type strList []string

func (s *strList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = nil
		*s = append(*s, splitList(n.Value)...)
		return nil
	case yaml.SequenceNode:
		*s = nil
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expected a word", c.Line)
			}
			*s = append(*s, c.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: expected a list", n.Line)
}

// applySidecar reads lane.N.yml at path, if present, over info.
func applySidecar(path string, info *Info) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var sf sideFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&sf); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w (keys: round, job, after, owner, created, guards, decisions, timeout, step-timeout, locks, stub, fail, outputs, includes, observe, cache, modules)", filepath.Base(path), err)
	}
	info.Exists = true
	name := filepath.Base(path)
	if sf.Round != nil {
		info.Round = strings.TrimSpace(*sf.Round)
	}
	if sf.Job != nil {
		info.Job = strings.TrimSpace(*sf.Job)
	}
	if sf.After != nil {
		info.After = nil
		for _, tok := range *sf.After {
			switch strings.ToLower(tok) {
			case "", "swim", "-", "none":
				continue
			}
			info.After = append(info.After, tok)
		}
	}
	if sf.Owner != nil {
		info.Owner = *sf.Owner
	}
	if sf.Created != nil {
		info.Created = *sf.Created
	}
	if sf.Timeout != nil {
		info.Timeout, info.TimeoutText = 0, *sf.Timeout
		if d, err := time.ParseDuration(*sf.Timeout); err != nil || d <= 0 {
			info.Problems = append(info.Problems, fmt.Sprintf("%s timeout %q is not a duration like 30m or 1h30m; no timeout applies", name, *sf.Timeout))
		} else {
			info.Timeout = d
		}
	}
	if sf.StepTimeout != nil {
		info.StepTimeout, info.StepTimeoutText = 0, ""
		if d, err := time.ParseDuration(*sf.StepTimeout); err != nil || d <= 0 {
			info.Problems = append(info.Problems, fmt.Sprintf("%s step-timeout %q is not a duration like 2m or 30s; no default step limit applies", name, *sf.StepTimeout))
		} else {
			info.StepTimeout, info.StepTimeoutText = d, *sf.StepTimeout
		}
	}
	if sf.Locks != nil {
		info.Locks = nil
		for _, tok := range *sf.Locks {
			if !ValidLockName(strings.NewReplacer("${", "", "}", "", "$", "").Replace(tok)) {
				info.Problems = append(info.Problems, fmt.Sprintf("%s locks: %q is not a lock name (letters, digits, . _ - /); ignored", name, tok))
				continue
			}
			info.Locks = append(info.Locks, tok)
		}
	}
	if len(sf.Guards) > 0 {
		flags := make([]string, 0, len(sf.Guards))
		for f := range sf.Guards {
			flags = append(flags, f)
		}
		sort.Strings(flags)
		listed := map[string]int{}
		for i, g := range info.Guards {
			listed[g.Flag] = i
		}
		var front []Guard
		for _, f := range flags {
			if f == "*" {
				// Flags named at run time (guard "$FLAG"): any may be asked.
				if info.DynamicGuards == 0 {
					info.DynamicGuards = 1
				}
				continue
			}
			if !isIdent(f) {
				info.Problems = append(info.Problems, fmt.Sprintf("%s guards: %q is not a flag name (letters, digits, _); ignored", name, f))
				continue
			}
			if i, ok := listed[f]; ok {
				info.Guards[i].Desc = sf.Guards[f]
				continue
			}
			front = append(front, Guard{Flag: f, Desc: sf.Guards[f]})
		}
		info.Guards = append(front, info.Guards...)
	}
	if len(sf.Decisions) > 0 {
		names := make([]string, 0, len(sf.Decisions))
		for d := range sf.Decisions {
			names = append(names, d)
		}
		sort.Strings(names)
		for _, d := range names {
			info.addDecision(d + "  " + sf.Decisions[d])
		}
		// The sidecar's description wins, as for guards.
		for i, d := range info.Decisions {
			if desc, ok := sf.Decisions[d.Flag]; ok {
				info.Decisions[i].Desc = desc
			}
		}
	}
	if sf.Stub != nil {
		info.Stub = *sf.Stub
	}
	if sf.Observe != nil {
		info.Observe = nil
		for _, v := range sf.Observe {
			info.addProbe(v, name+" observe:")
		}
	}
	if sf.Includes != nil {
		info.Includes = append([]string(nil), *sf.Includes...)
	}
	if sf.Modules != nil {
		// modules: is gone (swim lint: removed); read, so the sidecar loads.
		info.Removed = append(info.Removed, "Modules")
	}
	if sf.Cache != nil {
		info.Cache = nil
		info.setCache(strings.Join(*sf.Cache, " "), name+" cache:")
	}
	if sf.Outputs != nil {
		info.setOutputs(*sf.Outputs, name+" outputs:")
	}
	if sf.Fail != nil {
		info.setFail(*sf.Fail, name+" fail:")
	}
	// An older swim refuses a sidecar with keys it doesn't know, so these
	// keys need no syntax line: the round is syntax 3.
	if sf.Fail != nil {
		info.Syntax = max(info.Syntax, 3)
	}
	return nil
}

// EndPath is lane n's end request (.swim/laneN.end), for a lane running
// inside swim's wrapper: a failed `swim gate`, a `swim stop`, a refused
// step writes the round's exit code there and signals the wrapper
// (SIGUSR1), which ends the program even if it ignores the failure.
func EndPath(root string, n int) string {
	return filepath.Join(root, ".swim", fmt.Sprintf("lane%d.end", n))
}

// ClaimEnd records that lane n's round ends with code. Only the first
// claim counts (it returns false for later ones), so a round records one
// reason for ending.
func ClaimEnd(root string, n, code int) bool {
	os.MkdirAll(filepath.Join(root, ".swim"), 0o755)
	f, err := os.OpenFile(EndPath(root, n), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	fmt.Fprintf(f, "%d\n", code)
	return true
}

// EndCode is the exit code claimed for lane n's round, if one was.
func EndCode(root string, n int) (int, bool) {
	for i := 0; i < 5; i++ {
		data, err := os.ReadFile(EndPath(root, n))
		if err != nil {
			return 0, false
		}
		if c, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			return c, true
		}
		time.Sleep(20 * time.Millisecond) // claimed, not written yet
	}
	return 1, true
}

// ClearEnd removes lane n's end request.
func ClearEnd(root string, n int) { os.Remove(EndPath(root, n)) }
