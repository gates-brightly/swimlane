// Package ci is swim ci's CI side: which provider is running it (GitHub
// Actions, GitLab CI, or a generic runner), how that provider folds and
// annotates log output, which lane scripts a push or merge request changed,
// and the reports written after the run (per-lane sections, annotations, a
// job summary and JUnit XML), all read back from the lane logs.
package ci

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Annotation levels.
const (
	Error   = "error"
	Warning = "warning"
	Notice  = "notice"
)

// Provider formats output for one CI system.
type Provider interface {
	Name() string
	// Group starts a foldable section; collapsed is a hint (GitHub folds
	// every group, so callers leave a section they want open ungrouped).
	Group(w io.Writer, title string, collapsed bool)
	EndGroup(w io.Writer)
	// Annotate surfaces a message in the CI UI (or as a log line).
	Annotate(w io.Writer, level, title, msg string)
	// Folds reports whether Group really folds (a failed lane's section is
	// then left out of a group, so it shows expanded).
	Folds() bool
}

// Detect picks the provider from the environment; name ("github",
// "gitlab", "generic") overrides it.
func Detect(getenv func(string) string, name string) (Provider, error) {
	switch name {
	case "github":
		return GitHub{}, nil
	case "gitlab":
		return &GitLab{}, nil
	case "generic":
		return Generic{}, nil
	case "":
	default:
		return nil, fmt.Errorf("unknown provider %q: use github, gitlab or generic", name)
	}
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		return GitHub{}, nil
	case getenv("GITLAB_CI") == "true":
		return &GitLab{}, nil
	}
	return Generic{}, nil
}

// InCI reports whether a CI runner is detected (CI=true or a known provider).
func InCI(getenv func(string) string) bool {
	return getenv("CI") == "true" || getenv("CI") == "1" || getenv("GITHUB_ACTIONS") == "true" || getenv("GITLAB_CI") == "true"
}

// GitHub is GitHub Actions: ::group::, ::error and friends.
type GitHub struct{}

func (GitHub) Name() string { return "github" }
func (GitHub) Folds() bool  { return true }
func (GitHub) Group(w io.Writer, title string, _ bool) {
	fmt.Fprintf(w, "::group::%s\n", ghData(title))
}
func (GitHub) EndGroup(w io.Writer) { fmt.Fprintln(w, "::endgroup::") }
func (GitHub) Annotate(w io.Writer, level, title, msg string) {
	fmt.Fprintf(w, "::%s title=%s::%s\n", level, ghProp(title), ghData(msg))
}

// ghData escapes a workflow command's message.
func ghData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// ghProp escapes a workflow command's property value.
func ghProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// GitLab is GitLab CI: collapsible sections; it has no annotations (the
// JUnit report shows failures in merge requests), so they are log lines.
type GitLab struct {
	open []string
	n    int
}

func (*GitLab) Name() string { return "gitlab" }
func (*GitLab) Folds() bool  { return true }

var sectionNameRE = regexp.MustCompile(`[^a-z0-9_]+`)

func (g *GitLab) Group(w io.Writer, title string, collapsed bool) {
	g.n++
	name := fmt.Sprintf("swim_%d_%s", g.n, strings.Trim(sectionNameRE.ReplaceAllString(strings.ToLower(title), "_"), "_"))
	if len(name) > 60 {
		name = name[:60]
	}
	g.open = append(g.open, name)
	opt := ""
	if collapsed {
		opt = "[collapsed=true]"
	}
	fmt.Fprintf(w, "\x1b[0Ksection_start:%d:%s%s\r\x1b[0K%s\n", time.Now().Unix(), name, opt, title)
}

func (g *GitLab) EndGroup(w io.Writer) {
	if len(g.open) == 0 {
		return
	}
	name := g.open[len(g.open)-1]
	g.open = g.open[:len(g.open)-1]
	fmt.Fprintf(w, "\x1b[0Ksection_end:%d:%s\r\x1b[0K\n", time.Now().Unix(), name)
}

func (*GitLab) Annotate(w io.Writer, level, title, msg string) {
	Generic{}.Annotate(w, level, title, msg)
}

// Generic is any other runner: banners and plain "swim: error:" lines.
type Generic struct{}

func (Generic) Name() string { return "generic" }
func (Generic) Folds() bool  { return false }
func (Generic) Group(w io.Writer, title string, _ bool) {
	fmt.Fprintf(w, "==== %s ====\n", title)
}
func (Generic) EndGroup(w io.Writer) { fmt.Fprintln(w) }
func (Generic) Annotate(w io.Writer, level, title, msg string) {
	fmt.Fprintf(w, "swim: %s: %s: %s\n", level, title, msg)
}
