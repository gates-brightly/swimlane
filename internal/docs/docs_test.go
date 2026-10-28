package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// root is the repo's root directory.
func root(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// goFiles calls fn with the path and content of every .go file under dir
// (relative to the root), tests included or not.
func goFiles(t *testing.T, dir string, tests bool, fn func(path, src string)) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(root(t), dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") != tests {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fn(p, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The Go version the docs give for source builds is go.mod's.
func TestGoVersionMatchesGoMod(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`).FindStringSubmatch(read(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no go line")
	}
	for _, f := range []string{"docs/compatibility.md", "README.md"} {
		if !strings.Contains(read(t, f), "Go "+m[1]) && !strings.Contains(read(t, f), "Go **"+m[1]+"**") {
			t.Errorf("%s doesn't give go.mod's Go version (Go %s)", f, m[1])
		}
	}
}

// Every versioned schema swim writes (swim.<name>/vN in the code) has a
// stability tier in docs/compatibility.md.
func TestEverySchemaHasATier(t *testing.T) {
	doc := read(t, "docs/compatibility.md")
	re := regexp.MustCompile(`"(swim\.[a-z-]+/v\d+)"`)
	seen := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		goFiles(t, dir, false, func(_, src string) {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				seen[m[1]] = true
			}
		})
	}
	if len(seen) < 10 {
		t.Fatalf("found only %d schemas: %v", len(seen), seen)
	}
	var names []string
	for s := range seen {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		if !strings.Contains(doc, "`"+s+"`") {
			t.Errorf("docs/compatibility.md doesn't list schema %s", s)
		}
	}
}

// Every test the docs cite as proof exists.
func TestCitedTestsExist(t *testing.T) {
	funcs := map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	for _, dir := range []string{"internal", "cmd"} {
		goFiles(t, dir, true, func(_, src string) {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				funcs[m[1]] = true
			}
		})
	}
	cited := regexp.MustCompile("`(Test[A-Z]\\w*)`")
	docs, _ := filepath.Glob(filepath.Join(root(t), "docs", "*.md"))
	n := 0
	for _, p := range docs {
		data, _ := os.ReadFile(p)
		for _, m := range cited.FindAllStringSubmatch(string(data), -1) {
			n++
			if !funcs[m[1]] {
				t.Errorf("%s cites %s, which doesn't exist", filepath.Base(p), m[1])
			}
		}
	}
	if n < 10 {
		t.Errorf("the docs cite only %d tests; the trust model's table should cite more", n)
	}
}

// Relative links in the docs (and the README's links into docs/) point at
// files that exist.
func TestDocLinksResolve(t *testing.T) {
	link := regexp.MustCompile(`\]\(([^)#\s]+)(#[^)]*)?\)`)
	pages, _ := filepath.Glob(filepath.Join(root(t), "docs", "*.md"))
	pages = append(pages, filepath.Join(root(t), "README.md"), filepath.Join(root(t), "swimlane.md"))
	for _, p := range pages {
		data, _ := os.ReadFile(p)
		for _, m := range link.FindAllStringSubmatch(string(data), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(p), target)); err != nil {
				t.Errorf("%s links to %s, which doesn't exist", filepath.Base(p), target)
			}
		}
	}
}

// Anchors the docs link to exist (GitHub's heading slugs, or an explicit
// <a id>): links between the README and the pages in docs/, and within a
// page.
func TestDocAnchorsResolve(t *testing.T) {
	pages, _ := filepath.Glob(filepath.Join(root(t), "docs", "*.md"))
	pages = append(pages, filepath.Join(root(t), "README.md"), filepath.Join(root(t), "swimlane.md"))
	heading := regexp.MustCompile(`^#+ (.+)$`)
	ids := map[string]map[string]bool{} // page path -> anchors
	anchors := func(p string) map[string]bool {
		if m, ok := ids[p]; ok {
			return m
		}
		m := map[string]bool{}
		data, err := os.ReadFile(p)
		if err == nil {
			// Headings outside fenced code blocks.
			fenced := false
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "```") {
					fenced = !fenced
					continue
				}
				if h := heading.FindStringSubmatch(line); h != nil && !fenced {
					m[slug(h[1])] = true
				}
			}
			for _, a := range regexp.MustCompile(`<a id="([^"]+)">`).FindAllStringSubmatch(string(data), -1) {
				m[a[1]] = true
			}
		}
		ids[p] = m
		return m
	}
	link := regexp.MustCompile(`\]\(([^)#\s]*\.md)?#([^)\s]+)\)`)
	n := 0
	for _, p := range pages {
		data, _ := os.ReadFile(p)
		for _, m := range link.FindAllStringSubmatch(string(data), -1) {
			target := p
			if m[1] != "" {
				if strings.Contains(m[1], "://") {
					continue
				}
				target = filepath.Join(filepath.Dir(p), m[1])
			}
			n++
			if !anchors(target)[m[2]] {
				rel, _ := filepath.Rel(root(t), target)
				t.Errorf("%s links to %s#%s, which has no such heading", filepath.Base(p), filepath.ToSlash(rel), m[2])
			}
		}
	}
	if n < 10 {
		t.Errorf("found only %d anchored links; is the pattern right?", n)
	}
}

// slug is GitHub's anchor for a heading: lower case, punctuation dropped,
// spaces to hyphens.
func slug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// docs/usage.md embeds docs/architecture.md's diagram: they stay the same.
func TestUsageDiagramMatchesArchitecture(t *testing.T) {
	re := regexp.MustCompile("(?s)```mermaid\n.*?\n```")
	arch := re.FindString(read(t, "docs/architecture.md"))
	if arch == "" {
		t.Fatal("docs/architecture.md has no mermaid diagram")
	}
	if !strings.Contains(read(t, "docs/usage.md"), arch) {
		t.Error("docs/usage.md's diagram differs from docs/architecture.md's: copy it over")
	}
}

// docs/README.md is the index: every page in docs/ (and each SDK example)
// is listed in it.
func TestDocsIndexListsEveryPage(t *testing.T) {
	index := read(t, "docs/README.md")
	pages, _ := filepath.Glob(filepath.Join(root(t), "docs", "*.md"))
	examples, _ := filepath.Glob(filepath.Join(root(t), "docs", "examples", "*.md"))
	for _, p := range append(pages, examples...) {
		rel, _ := filepath.Rel(filepath.Join(root(t), "docs"), p)
		if rel == "README.md" {
			continue
		}
		if !strings.Contains(index, "]("+filepath.ToSlash(rel)+")") {
			t.Errorf("docs/README.md doesn't list %s", rel)
		}
	}
}
