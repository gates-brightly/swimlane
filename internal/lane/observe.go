package lane

import (
	"fmt"
	"strings"
)

// Observe: lines declare facts a round depends on that swim can't pin or
// stage, such as the binary PATH finds for a command or the cloud account
// the credentials reach:
//
//	# Observe: aws_account = aws sts get-caller-identity --query Account --output text
//	# Observe: path terraform aws
//
// A probe is read when a guard is approved and again just before the guard
// returns; a difference ends the round as drift. Probes narrow the gap
// between approval and use; they don't close it, and they must have no side
// effects (swim lint warns about ones that look like they do).

// Probe is one Observe: line: a named command whose output is the fact, or
// (Path) commands whose resolved binary and its hash are.
type Probe struct {
	Name string   // the fact's name; "" for a path probe
	Cmd  string   // the command, run with bash -c in the repo root
	Path []string // path probe: command names looked up on PATH
	Line int      // where it is declared (1-based; 0 in a sidecar)
}

// probeName reports whether s names a probe or a command: a letter or _,
// then letters, digits, _ . and -.
func probeName(s string) bool {
	return s != "" && (isLetter(rune(s[0])) || s[0] == '_') &&
		allOf(s, func(r rune) bool { return isLetter(r) || isDigit(r) || strings.ContainsRune("_.-", r) })
}

// ParseProbe reads one Observe: value: "NAME = COMMAND" or "path CMD...".
func ParseProbe(v string) (Probe, error) {
	v = strings.TrimSpace(v)
	if f := strings.Fields(v); len(f) > 0 && f[0] == "path" && !strings.Contains(v, "=") {
		if len(f) == 1 {
			return Probe{}, fmt.Errorf("Observe: path needs command names, e.g. `path terraform aws`")
		}
		for _, c := range f[1:] {
			if !probeName(c) {
				return Probe{}, fmt.Errorf("Observe: path %q is not a command name", c)
			}
		}
		return Probe{Path: f[1:]}, nil
	}
	name, cmd, ok := strings.Cut(v, "=")
	name, cmd = strings.TrimSpace(name), strings.TrimSpace(cmd)
	if !ok || cmd == "" || !probeName(name) {
		return Probe{}, fmt.Errorf("Observe: %q is not `NAME = command` or `path CMD...`", v)
	}
	return Probe{Name: name, Cmd: cmd}, nil
}

// Names are the facts a probe reports: its name, or path.CMD for each
// command of a path probe.
func (p Probe) Names() []string {
	if p.Name != "" {
		return []string{p.Name}
	}
	out := make([]string, len(p.Path))
	for i, c := range p.Path {
		out[i] = "path." + c
	}
	return out
}

func (i *Info) addProbe(v, where string) {
	if v == "" || v == "-" || strings.EqualFold(v, "none") {
		return
	}
	p, err := ParseProbe(v)
	if err != nil {
		i.Problems = append(i.Problems, strings.Replace(err.Error(), "Observe:", where, 1)+"; ignored")
		return
	}
	i.Observe = append(i.Observe, p)
}

// ProbesIn are the Observe: probes of a lane script, from its content (a
// staged copy) and its sidecar at sidecar ("" for none).
func ProbesIn(data []byte, sidecar string) []Probe {
	var info Info
	if !IsBinary(data) {
		info = ParseScript(string(data))
	}
	if sidecar != "" {
		applySidecar(sidecar, &info)
	}
	return info.Observe
}
