package lint

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
)

// headerKeys are the header keys swim reads, spelled as it reads them
// (case matters: # timeout: is not # Timeout:).
var headerKeys = []string{"Round", "Task", "Round-ID", "Job", "After", "Owner", "Created", "Guards", "Timeout", "Step-Timeout",
	"Locks", "Includes", "Targets", "Signoff", "Workspace", "Fail", "swim"}

// headerSynonyms are words agents reach for in place of a key swim reads.
var headerSynonyms = map[string]string{"depends": "After", "depends-on": "After", "dependson": "After", "requires": "After",
	"needs": "After", "waits": "After", "wait": "After", "lock": "Locks", "include": "Includes", "guard": "Guards",
	"target": "Targets", "step-timeouts": "Step-Timeout", "steptimeout": "Step-Timeout"}

// checkHeaderKey: a key in the header block that swim doesn't read but
// that looks meant as one it does (another case, a synonym, a typo) is
// dropped silently at run time: # Depends: 2 orders nothing, # Lock: db
// locks nothing. Other keys stay the round's own notes, shown as written.
func checkHeaderKey(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for i, raw := range s.v.raw {
		line := strings.TrimSpace(raw)
		if i == 0 && strings.HasPrefix(line, "#!") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break // the header block ends at the first line of code
		}
		key, value, ok := lane.HeaderKey(line)
		if !ok || isHeaderKey(key) {
			continue
		}
		want := meantKey(key)
		if want == "After" && !strings.EqualFold(key, "After") && !looksLikeRefs(value) {
			// # Requires: VPN, # Wait: for the change window: a note, not
			// an After: (whose values are lanes and job ids).
			want = ""
		}
		if want != "" {
			why := "swim doesn't read it"
			if strings.EqualFold(key, want) {
				why = "header keys are case-sensitive"
			}
			out = append(out, f(finding.Error, "header-key", i, "write # "+want+":",
				"# %s: is dropped (%s): did you mean # %s:?", key, why, want))
		}
	}
	return out
}

// checkJobIsRoundID: # Job: is # Round-ID:'s old name; swim still reads
// it in an older script.
func checkJobIsRoundID(c *Context, s *Script) []finding.Finding {
	if i := jobHeaderLine(s.v.raw); i >= 0 {
		return []finding.Finding{f(finding.Warn, "job-is-round-id", i, "write # Round-ID: (swim lint --fix rewrites it)",
			"# Job: is now # Round-ID:")}
	}
	return nil
}

// jobHeaderLine is the 0-based line of the header's # Job: (or // Job:)
// line, or -1.
func jobHeaderLine(lines []string) int {
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if i == 0 && strings.HasPrefix(line, "#!") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") {
			break
		}
		if rest, ok := strings.CutPrefix(line, "//"); ok {
			line = "#" + rest
		}
		if key, _, ok := lane.HeaderKey(line); ok && key == "Job" {
			return i
		}
	}
	return -1
}

// renameJob rewrites src's # Job: header line to # Round-ID: (what swim
// lint --fix does), and returns the 1-based line it changed (0: none).
func renameJob(src string) (string, int) {
	lines := strings.Split(src, "\n")
	i := jobHeaderLine(lines)
	if i < 0 {
		return src, 0
	}
	at := strings.Index(lines[i], "Job:")
	lines[i] = lines[i][:at] + "Round-ID:" + lines[i][at+len("Job:"):]
	return strings.Join(lines, "\n"), i + 1
}

// looksLikeRefs reports whether a header value is lanes or job ids, as
// After: takes them: 2 3, 2,3, 3f2a9c1e-7b4d.
func looksLikeRefs(v string) bool {
	toks := strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' })
	if len(toks) == 0 {
		return false
	}
	for _, t := range toks {
		if !chars.All(t, chars.Digit) && !(len(t) >= 8 && lane.ValidJobID(t) && strings.ContainsAny(t, "0123456789")) {
			return false
		}
	}
	return true
}

func isHeaderKey(k string) bool {
	for _, h := range headerKeys {
		if k == h {
			return true
		}
	}
	return false
}

// meantKey is the key swim reads that k looks meant as, or "".
func meantKey(k string) string {
	lk := strings.ToLower(k)
	if want := headerSynonyms[lk]; want != "" {
		return want
	}
	best, bestD := "", 99
	for _, h := range headerKeys {
		if h == "swim" {
			continue
		}
		d := editDistance(lk, strings.ToLower(h))
		if d < bestD {
			best, bestD = h, d
		}
	}
	limit := 1
	if len(lk) >= 7 {
		limit = 2
	}
	if bestD <= limit {
		return best
	}
	return ""
}

// editDistance is the edit distance between a and b, a swap of two
// neighbouring letters counting as one edit (Gaurds is one from Guards).
func editDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

// timeoutProblem reports whether a header problem is an unreadable time
// limit: the round would run with none, so it's an error, not a warning.
func timeoutProblem(p string) bool {
	return strings.HasPrefix(p, "Timeout ") || strings.HasPrefix(p, "Step-Timeout ")
}

// headerCommentRE: a # comment after a header value (whitespace, #, then
// whitespace or the end). A # that starts a word (#42) is text.
var headerCommentRE = regexp.MustCompile(`\s#(\s|$)`)

// checkHeaderComment: a header swim reads takes a value and nothing else.
// A trailing # comment is read as part of the value (# After: 2  # db
// makes "#" and "db" lanes or jobs to wait for), so it's an error on every
// such key alike, one rule to know: notes go on their own line. Your own
// keys (# Goal:, # Issue:) are notes, shown as written.
func checkHeaderComment(c *Context, s *Script) []finding.Finding {
	var out []finding.Finding
	for i, raw := range s.v.raw {
		line := strings.TrimSpace(raw)
		if i == 0 && strings.HasPrefix(line, "#!") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break // the header block ends at the first line of code
		}
		key, value, ok := lane.HeaderKey(line)
		if !ok || key == "swim" || !isHeaderKey(key) {
			continue // # swim: syntax 4; your own notes (# Goal:, # Issue:) are shown as written
		}
		if loc := headerCommentRE.FindStringIndex(value); loc != nil {
			out = append(out, f(finding.Error, "header-comment", i,
				fmt.Sprintf("# %s: %s, and the note on a line of its own (# Goal: ...)", key, strings.TrimSpace(value[:loc[0]])),
				"# %s: carries a # comment: a header value takes nothing else, and the comment would be read as part of it", key))
		}
	}
	return out
}
