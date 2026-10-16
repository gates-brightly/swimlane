package lint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A lane's code isn't only its script: bash lanes source local files, and
// Python and TypeScript lanes import local modules. # Includes: is what the
// manifest pins and stages, and the run refuses a repo file it didn't (a
// bash source of one) or can't find (an import). This static scan is
// lint's: it finds the files a script reads, so lint can say which # Includes:
// misses (and swim lint --fix can add them). It's advice; nothing in a run
// depends on it.

// Source is one local file a script reads as code, or a source line whose
// file swim can't know ahead (Dynamic).
type Source struct {
	Path    string // repo-relative ("" when Dynamic)
	Line    int    // 1-based
	Dynamic bool   // source "$x": named at run time
	Arg     string // the argument as written
	From    string // "source" (bash) or "import" (Python, TypeScript)
}

var (
	sourceRE   = regexp.MustCompile(`(?:^|[;&|({]\s*|\b(?:then|do|else)\s+)(?:source|\.)\s+("[^"]*"|'[^']*'|[^\s;&|)]+)`)
	pyImportRE = regexp.MustCompile(`^\s*(?:from\s+(\.?[A-Za-z_][\w.]*)\s+import|import\s+([A-Za-z_][\w.]*))`)
	tsImportRE = regexp.MustCompile(`(?:from|import)\s*\(?\s*["'](\.{1,2}/[^"']+)["']`)
)

// ImportCandidates are the repo-relative files an import line of a Python
// or TypeScript lane may name, most likely first (nil for another line or
// language, or the swim SDK).
func importCandidates(ext, line string) []string {
	switch strings.ToLower(ext) {
	case ".py":
		m := pyImportRE.FindStringSubmatch(line)
		if m == nil {
			return nil
		}
		mod := strings.TrimPrefix(m[1]+m[2], ".")
		if mod == "swim" {
			return nil
		}
		p := strings.ReplaceAll(mod, ".", "/")
		return []string{p + ".py", p + "/__init__.py"}
	case ".ts", ".mts", ".js", ".mjs":
		m := tsImportRE.FindStringSubmatch(line)
		if m == nil {
			return nil
		}
		p := filepath.ToSlash(filepath.Clean(m[1]))
		return []string{p, p + ".ts", p + ".js", p + "/index.ts", p + "/index.js"}
	}
	return nil
}

// ScanSources lists the local files src (a script called name) reads as
// code: bash source and . lines, Python and TypeScript imports. Only files
// that exist under root count; a bash source of a path built at run time is
// reported as Dynamic.
func scanSources(root, name, src string) []Source {
	ext := strings.ToLower(filepath.Ext(name))
	bash := ext == ".sh" || ext == ".bash" || ext == ""
	var out []Source
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if !bash {
			for _, cand := range importCandidates(ext, line) {
				if isFile(filepath.Join(root, cand)) {
					out = append(out, Source{Path: cand, Line: i + 1, Arg: cand, From: "import"})
					break
				}
			}
			continue
		}
		for _, m := range sourceRE.FindAllStringSubmatch(line, -1) {
			arg := m[1]
			word := strings.Trim(arg, `"'`)
			if strings.ContainsAny(word, "$`*?[") && !strings.HasPrefix(arg, "'") || strings.HasPrefix(word, "~") {
				if !strings.HasPrefix(word, "~") {
					out = append(out, Source{Line: i + 1, Dynamic: true, Arg: arg, From: "source"})
				}
				continue
			}
			if rel, ok := repoRel(root, word); ok && isFile(filepath.Join(root, rel)) {
				out = append(out, Source{Path: rel, Line: i + 1, Arg: arg, From: "source"})
			}
		}
	}
	return out
}

// repoRel is path relative to root, if it is inside it (a relative path
// is relative to root: lanes run in the repo root).
func repoRel(root, path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", false
		}
		path = rel
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return "", false
	}
	return path, true
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
