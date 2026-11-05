// Package helpers holds the lane helper modules swim ships: bash files a
// repo copies in (swim helpers add) and its lanes include and source.
//
// A copy starts with a stamp line naming its module, the swim it came
// from and the SHA-256 of the rest of the file:
//
//	# swim helpers: gh, from swim 0.20261106.1, sha256 <hex>
//
// so swim can tell a copy as written (current, or older than this swim's)
// from one edited since.
package helpers

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed *.sh
var files embed.FS

// Modules are the module names swim ships, sorted.
func Modules() []string {
	entries, _ := files.ReadDir(".")
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".sh"); ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Source is module m's file as swim ships it (no stamp).
func Source(m string) (string, bool) {
	b, err := files.ReadFile(m + ".sh")
	return string(b), err == nil
}

// Summary is module m's one-line description: the first sentence of its
// header comment ("# swim helpers, module gh: GitHub. ..." is "GitHub").
func Summary(m string) string {
	src, _ := Source(m)
	var head []string
	for _, line := range strings.Split(src, "\n") {
		c, ok := strings.CutPrefix(line, "#")
		if c = strings.TrimSpace(c); !ok || c == "" {
			break
		}
		head = append(head, c)
	}
	_, rest, ok := strings.Cut(strings.Join(head, " "), "module "+m+": ")
	if !ok {
		return ""
	}
	if s, _, cut := strings.Cut(rest, ". "); cut {
		rest = s
	}
	return strings.TrimSuffix(rest, ".")
}

// Stamped is module m's file with its stamp, as swim helpers add writes it.
func Stamped(m, swimVersion string) (string, bool) {
	src, ok := Source(m)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("# swim helpers: %s, from swim %s, sha256 %s\n", m, swimVersion, hash(src)) + src, true
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var stampRE = regexp.MustCompile(`^# swim helpers: ([a-z0-9_]+), from swim (\S+), sha256 ([0-9a-f]{64})$`)

// State is how a copy stands against this swim's module.
type State int

const (
	NotHelper State = iota // no stamp: not a copy swim wrote
	Current                // as this swim ships it
	Differs                // as written, but this swim ships a different one (older or newer)
	Edited                 // changed since swim helpers add wrote it
	Unknown                // stamped with a module this swim doesn't ship
)

// Copy is what a file's stamp says, and how it stands.
type Copy struct {
	Module string
	From   string // the swim it came from
	State  State
}

// Check reads a file's contents as a helper copy.
func Check(content string) Copy {
	first, rest, _ := strings.Cut(content, "\n")
	m := stampRE.FindStringSubmatch(first)
	if m == nil {
		return Copy{State: NotHelper}
	}
	c := Copy{Module: m[1], From: m[2]}
	src, ok := Source(c.Module)
	switch {
	case hash(rest) != m[3]:
		c.State = Edited
	case !ok:
		c.State = Unknown
	case rest == src:
		c.State = Current
	default:
		c.State = Differs
	}
	return c
}

// Describe says how c stands against swim version this, for swim helpers
// and swim lint.
func (c Copy) Describe(this string) string {
	switch c.State {
	case Current:
		return fmt.Sprintf("%s, current (swim %s)", c.Module, c.From)
	case Differs:
		return fmt.Sprintf("%s from swim %s; this swim (%s) ships a different %s: swim helpers add %s updates it", c.Module, c.From, this, c.Module, c.Module)
	case Edited:
		return fmt.Sprintf("%s from swim %s, edited since swim helpers add wrote it", c.Module, c.From)
	case Unknown:
		return fmt.Sprintf("%s from swim %s, a module this swim doesn't ship", c.Module, c.From)
	}
	return "not a swim helpers copy"
}

// Func is one documented entry of a module: the comment block above its
// public functions, "# gh_pr_fetch N [REMOTE]: a gate that ...". Usage is
// the part before ": " (one or more functions with their arguments), Doc
// the rest as one paragraph. A "# Changes: WHAT" line in the block says
// the function changes something (Changes is WHAT): lint holds a lane's
// call of it to the rules for a destructive command.
type Func struct {
	Names   []string // the functions it defines: gh_pr_fetch; os_mac, os_linux
	Usage   string
	Doc     string
	Changes string
}

// Funcs are module m's documented public functions, in file order. A
// function defined right after another under one comment
// ("# os_mac / os_linux: ...") shares its entry.
func Funcs(m string) []Func {
	src, _ := Source(m)
	return FuncsIn(src)
}

// FuncsIn are the documented public functions of a module's source, swim's
// or a repo's own, written as swim's are.
func FuncsIn(src string) []Func {
	var out []Func
	var block []string
	var cur *Func
	for _, line := range strings.Split(src, "\n") {
		if c, ok := strings.CutPrefix(line, "#"); ok {
			block = append(block, strings.TrimPrefix(c, " "))
			cur = nil
			continue
		}
		name, isFunc := strings.CutSuffix(line, "() {")
		switch {
		case isFunc && !strings.HasPrefix(name, "_") && len(block) > 0 && strings.HasPrefix(block[0], name):
			var doc []string
			changes := ""
			for _, b := range block {
				if w, ok := strings.CutPrefix(b, "Changes: "); ok {
					changes = strings.TrimSuffix(strings.TrimSpace(w), ".")
					continue
				}
				doc = append(doc, b)
			}
			usage, text, _ := strings.Cut(strings.Join(doc, " "), ": ")
			if text != "" {
				text = strings.ToUpper(text[:1]) + text[1:]
			}
			out = append(out, Func{Names: []string{name}, Usage: usage, Doc: text, Changes: changes})
			cur = &out[len(out)-1]
		case isFunc && cur != nil && strings.Contains(cur.Usage, name):
			cur.Names = append(cur.Names, name)
		}
		if strings.TrimSpace(line) == "" || isFunc {
			block = nil
		}
	}
	return out
}
