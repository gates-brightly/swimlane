// Package evidence keeps what a round's evidence steps showed (the output
// of `evidence "tf plan" terraform plan ...` in its check stage), so an
// approval can cover the plan, not just the script. Output is normalized
// (ANSI stripped by the step, line endings unified, trailing whitespace
// cut), saved per job in .swim/evidence/<job>/, and summed up as one hash
// for the round: the SHA-256 of its sorted "label:hash" lines. The round's
// whole evidence text is also kept by that hash under blobs/, so a changed
// plan can be shown as a diff against the approved one.
package evidence

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Step is one evidence step's result.
type Step struct {
	Label string `yaml:"label"`
	Hash  string `yaml:"sha256"`
	File  string `yaml:"file"` // relative to the job's directory
}

// Index is a job's latest evidence: from one run, for one script.
type Index struct {
	Job    string `yaml:"job"`
	Run    string `yaml:"run"`
	Script string `yaml:"script_sha256"`
	At     string `yaml:"at"`
	Check  bool   `yaml:"check,omitempty"` // gathered by swim plan --check
	Steps  []Step `yaml:"steps"`
	Hash   string `yaml:"sha256"` // the round's evidence hash
}

// Dir is job's evidence directory.
func Dir(root, job string) string { return filepath.Join(root, ".swim", "evidence", job) }

func blobDir(root string) string { return filepath.Join(root, ".swim", "evidence", "blobs") }

// Normalize is the text an evidence hash covers: line endings unified and
// trailing whitespace (on each line, and at the end) removed.
func Normalize(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func sum(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func slug(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Load reads job's latest evidence (nil if it has none).
func Load(root, job string) *Index { return loadFile(filepath.Join(Dir(root, job), "evidence.yml")) }

// LaneKey names a lane's latest evidence, whatever its job: "3".
func LaneKey(n int) string { return fmt.Sprint(n) }

func laneIndex(root, key string) string {
	return filepath.Join(root, ".swim", "evidence", "lanes", key+".yml")
}

// LoadLane reads a lane's latest evidence (nil if it has none): for
// approving a script whose rounds have no fixed job.
func LoadLane(root, key string) *Index { return loadFile(laneIndex(root, key)) }

func loadFile(path string) *Index {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ix Index
	if yaml.Unmarshal(data, &ix) != nil {
		return nil
	}
	return &ix
}

// Record saves one evidence step's output for job's round in run (of the
// script with hash script) and returns the round's evidence hash so far.
// The first step of a new run starts the index afresh.
func Record(root, laneKey, job, run, script, label, output string, check bool) (string, error) {
	dir := Dir(root, job)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ix := Load(root, job)
	if ix == nil || ix.Run != run {
		ix = &Index{Job: job, Run: run}
	}
	ix.Script, ix.At, ix.Check = script, time.Now().UTC().Format(time.RFC3339), check
	text := Normalize(output)
	name := slug(label)
	if name == "" {
		name = "evidence"
	}
	file := name + ".txt"
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		return "", err
	}
	kept := ix.Steps[:0]
	for _, s := range ix.Steps {
		if s.Label != label {
			kept = append(kept, s)
		}
	}
	ix.Steps = append(kept, Step{Label: label, Hash: sum(text), File: file})
	sort.Slice(ix.Steps, func(a, b int) bool { return ix.Steps[a].Label < ix.Steps[b].Label })
	ix.Hash = roundHash(ix.Steps)
	// Keep the round's whole evidence by its hash, for diffs later.
	if blob, err := ix.Text(root); err == nil {
		os.MkdirAll(blobDir(root), 0o755)
		os.WriteFile(filepath.Join(blobDir(root), ix.Hash+".txt"), []byte(blob), 0o644)
	}
	data, err := yaml.Marshal(ix)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("evidence.yml.%d.tmp", os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "evidence.yml")); err != nil {
		return "", err
	}
	// The lane's latest evidence points at this job's.
	if laneKey != "" {
		li := laneIndex(root, laneKey)
		os.MkdirAll(filepath.Dir(li), 0o755)
		os.WriteFile(li, data, 0o644)
	}
	return ix.Hash, nil
}

func roundHash(steps []Step) string {
	lines := make([]string, len(steps))
	for i, s := range steps {
		lines[i] = s.Label + ":" + s.Hash
	}
	return sum(strings.Join(lines, "\n") + "\n")
}

// Text is the round's evidence as one text: each step under "== <label>".
func (ix *Index) Text(root string) (string, error) {
	var b strings.Builder
	for _, s := range ix.Steps {
		data, err := os.ReadFile(filepath.Join(Dir(root, ix.Job), s.File))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "== %s\n%s", s.Label, data)
	}
	return b.String(), nil
}

// Blob is the evidence text kept for hash ("" if it isn't kept).
func Blob(root, hash string) string {
	data, _ := os.ReadFile(filepath.Join(blobDir(root), hash+".txt"))
	return string(data)
}

// Diff is a line diff of old against new: "- " for lines only in old, "+ "
// for lines only in new, around unchanged context (at most max lines).
func Diff(old, new string, max int) []string {
	a, b := strings.Split(strings.TrimRight(old, "\n"), "\n"), strings.Split(strings.TrimRight(new, "\n"), "\n")
	// Longest common subsequence, for small texts; evidence is a plan
	// summary, not a file tree.
	n, m := len(a), len(b)
	if n*m > 4_000_000 {
		return []string{fmt.Sprintf("(evidence too large to diff: %d and %d lines)", n, m)}
	}
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = maxInt(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+ "+b[j])
			j++
		default:
			out = append(out, "- "+a[i])
			i++
		}
		if len(out) >= max {
			out = append(out, "  (more changes not shown)")
			break
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
