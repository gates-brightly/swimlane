// Package finding is the shared result type of `swim lint` and
// `swim doctor`: a problem at a level, with where it is and how to fix it,
// rendered as text or as a versioned YAML document.
package finding

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Levels, most severe first.
const (
	Error = "error"
	Warn  = "warn"
	Info  = "info"
)

// Finding is one problem found by a check.
type Finding struct {
	Level   string `yaml:"level"`
	File    string `yaml:"file,omitempty"` // relative to the repo root where possible
	Line    int    `yaml:"line,omitempty"` // 1-based; 0 for the whole file
	Code    string `yaml:"code"`           // stable check id, e.g. set-e
	Message string `yaml:"message"`
	Fix     string `yaml:"fix,omitempty"`
}

// Where is "file:line", "file", or "" for findings about no file.
func (f Finding) Where() string {
	switch {
	case f.File == "":
		return ""
	case f.Line > 0:
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

func rank(level string) int {
	switch level {
	case Error:
		return 0
	case Warn:
		return 1
	}
	return 2
}

// Sort orders findings by file, line, then severity.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(a, b int) bool {
		x, y := fs[a], fs[b]
		if x.File != y.File {
			return lessFile(x.File, y.File)
		}
		if x.Line != y.Line {
			return x.Line < y.Line
		}
		return rank(x.Level) < rank(y.Level)
	})
}

// lessFile sorts lane.2.sh before lane.10.sh, and files before "".
func lessFile(a, b string) bool {
	if a == "" || b == "" {
		return b == ""
	}
	var na, nb int
	if _, err := fmt.Sscanf(a, "lane.%d.sh", &na); err == nil {
		if _, err := fmt.Sscanf(b, "lane.%d.sh", &nb); err == nil && na != nb {
			return na < nb
		}
	}
	return a < b
}

// Count returns how many findings are at each level.
func Count(fs []Finding) (errors, warnings, infos int) {
	for _, f := range fs {
		switch f.Level {
		case Error:
			errors++
		case Warn:
			warnings++
		default:
			infos++
		}
	}
	return
}

// ExitCode is 1 when any finding is an error (or, with strict, a warning).
func ExitCode(fs []Finding, strict bool) int {
	e, w, _ := Count(fs)
	if e > 0 || strict && w > 0 {
		return 1
	}
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Summary is the closing line, e.g. "2 errors, 2 warnings, 1 info".
func Summary(fs []Finding) string {
	e, w, i := Count(fs)
	if e+w+i == 0 {
		return "no problems found"
	}
	parts := []string{plural(e, "error", "errors"), plural(w, "warning", "warnings")}
	if i > 0 {
		parts = append(parts, plural(i, "info", "infos"))
	}
	return strings.Join(parts, ", ")
}

// WriteText prints one finding per line, aligned, with its fix indented
// below, then the summary line.
func WriteText(w io.Writer, fs []Finding) {
	width := 0
	for _, f := range fs {
		if n := len(f.Where()); n > width {
			width = n
		}
	}
	for _, f := range fs {
		where := f.Where()
		if where == "" {
			where = "-"
		}
		fmt.Fprintf(w, "%-*s  %-5s  %s  [%s]\n", width, where, f.Level, f.Message, f.Code)
		if f.Fix != "" {
			fmt.Fprintf(w, "%-*s         fix: %s\n", width, "", f.Fix)
		}
	}
	fmt.Fprintln(w, Summary(fs))
}

// Document is the --yaml output.
type Document struct {
	Schema   string    `yaml:"schema"`
	Errors   int       `yaml:"errors"`
	Warnings int       `yaml:"warnings"`
	Infos    int       `yaml:"infos"`
	Findings []Finding `yaml:"findings"`
}

// WriteYAML prints the findings as a YAML document with the given schema
// (swim.lint/v1 or swim.doctor/v1).
func WriteYAML(w io.Writer, schema string, fs []Finding) error {
	d := Document{Schema: schema, Findings: fs}
	if d.Findings == nil {
		d.Findings = []Finding{}
	}
	d.Errors, d.Warnings, d.Infos = Count(fs)
	out, err := yaml.Marshal(d)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}
