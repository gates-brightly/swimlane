// Package chars checks and cleans text by character class: names, flags,
// numbers, slugs. swim uses it instead of regular expressions for anything
// a few character tests can say.
package chars

import "strings"

// Letter reports whether r is an ASCII letter.
func Letter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

// Lower reports whether r is a lower-case ASCII letter.
func Lower(r rune) bool { return r >= 'a' && r <= 'z' }

// Digit reports whether r is an ASCII digit.
func Digit(r rune) bool { return r >= '0' && r <= '9' }

// Alnum reports whether r is an ASCII letter or digit.
func Alnum(r rune) bool { return Letter(r) || Digit(r) }

// In returns a test for r being one of set.
func In(set string) func(rune) bool { return func(r rune) bool { return strings.ContainsRune(set, r) } }

// Or combines tests.
func Or(tests ...func(rune) bool) func(rune) bool {
	return func(r rune) bool {
		for _, t := range tests {
			if t(r) {
				return true
			}
		}
		return false
	}
}

// All reports whether every rune of s passes ok (true for "").
func All(s string, ok func(rune) bool) bool {
	for _, r := range s {
		if !ok(r) {
			return false
		}
	}
	return true
}

// Word reports whether s is non-empty, starts with a rune passing first,
// and the rest pass rest.
func Word(s string, first, rest func(rune) bool) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 && !first(r) || i > 0 && !rest(r) {
			return false
		}
	}
	return true
}

// Ident reports whether s is a shell variable (or guard flag) name: a
// letter or _, then letters, digits and _.
func Ident(s string) bool {
	return Word(s, Or(Letter, In("_")), Or(Alnum, In("_")))
}

// Digits reports whether s is one or more ASCII digits.
func Digits(s string) bool { return s != "" && All(s, Digit) }

// Squash replaces every run of runes that fail keep with sep, and trims
// sep from both ends ("Orders API v2" with Alnum and "-": "Orders-API-v2").
func Squash(s string, keep func(rune) bool, sep string) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		if keep(r) {
			if gap && b.Len() > 0 {
				b.WriteString(sep)
			}
			b.WriteRune(r)
			gap = false
		} else {
			gap = true
		}
	}
	return b.String()
}
