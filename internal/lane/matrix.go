package lane

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/cache"
)

// Matrix rounds: one lane script runs once per value (a cell), each cell
// with its own log, pid, status entry, job (<job>/<cell>) and result.
//
//	# Matrix: SVC=orders,billing,users     (several lines: the cross product)
//	# Matrix: SVC=@services.txt            (one value per line, from a repo file)
//	# Matrix-Exclude: ENV=prod,SVC=search  (drop cells matching every pair)
//	# Matrix-Parallel: 2                   (cells at once; default 1)
//	# Matrix-Fail: stop                    (skip cells not started after a failure)
//
// A cell's process has SWIM_CELL set (its name) and each key as an env var;
// swim's hooks then read and write that cell's files and status entry.

// CellEnv names the cell a lane process runs (empty for a plain lane).
const CellEnv = "SWIM_CELL"

// MatrixJobEnv carries the job the launcher made for a matrix round with
// no Job: line, so its cells share it (<job>/<cell>).
const MatrixJobEnv = "SWIM_MATRIX_JOB"

// MaxCells is the most cells a matrix expands to.
const MaxCells = 256

// CurrentCell is the cell this process runs ("" for a plain lane).
func CurrentCell() string { return os.Getenv(CellEnv) }

// CellOf is the current process's cell when it belongs to lane n, else "".
// A command run inside a job or cell of one lane (swim lint in job 1.2)
// that reads another lane reads that lane whole, not "its" cell 2.
func CellOf(n int) string {
	c := CurrentCell()
	if c == "" {
		return ""
	}
	if l, err := strconv.Atoi(os.Getenv("SWIM_LANE")); err == nil && l != n {
		return ""
	}
	return c
}

// Axis is one Matrix: line.
type Axis struct {
	Key    string
	Values []string
	File   string // @file the values came from ("" if listed inline)
}

// Cell is one combination of matrix values.
type Cell struct {
	Name string // "orders", or "ENV=prod,SVC=orders" for a cross product
	Vars []KV   // KEY=value, in axis order
}

// Env is the cell's variables as KEY=VALUE, plus SWIM_CELL and
// SWIM_CELL_LOG (its log, for the bash library).
func (c Cell) Env(root string, n int) []string {
	out := []string{CellEnv + "=" + c.Name, "SWIM_CELL_LOG=" + CellLog(root, n, c.Name)}
	for _, kv := range c.Vars {
		out = append(out, kv.K+"="+kv.V)
	}
	return out
}

// Slug is the cell's name as a file name fragment: "orders",
// "env-prod.svc-orders".
func (c Cell) Slug() string { return CellSlug(c.Name) }

// CellSlug is a cell name as a file name fragment (a job's path, 5.9.9, as
// it is).
func CellSlug(name string) string {
	if IsJobPath(name) {
		return name
	}
	parts := strings.Split(name, ",")
	for i, p := range parts {
		parts[i] = Slug(p)
	}
	return strings.Join(parts, ".")
}

// cellValue reports whether v can be a matrix value: letters, digits and
// . _ : / @ + -.
func cellValue(v string) bool {
	return v != "" && allOf(v, func(r rune) bool { return isLetter(r) || isDigit(r) || strings.ContainsRune("._:/@+-", r) })
}

// ParseCellRef splits "3[orders]" into "3" and "orders", and a group
// lane's sub-job "1.2" into "1" and "2"; ok is false for a plain lane or
// job reference.
func ParseCellRef(s string) (ref, cell string, ok bool) {
	// "3[orders]": the reference, then the cell in brackets.
	if i := strings.IndexByte(s, '['); i > 0 && strings.HasSuffix(s, "]") && len(s)-1 > i+1 {
		return s[:i], s[i+1 : len(s)-1], true
	}
	// "1.2": a lane of jobs and a job's place under it.
	if n, path, ok := strings.Cut(s, "."); ok && isDigits(n) && IsJobPath(path) {
		return n, path, true
	}
	return s, "", false
}

// parseAxis reads "KEY=a,b,c" or "KEY=@file".
func parseAxis(v string) (Axis, error) {
	k, vals, ok := strings.Cut(v, "=")
	k = strings.TrimSpace(k)
	if !ok || !isIdent(k) {
		return Axis{}, fmt.Errorf("Matrix %q: write KEY=a,b,c or KEY=@file", v)
	}
	vals = strings.TrimSpace(vals)
	if strings.HasPrefix(vals, "@") {
		return Axis{Key: k, File: strings.TrimPrefix(vals, "@")}, nil
	}
	a := Axis{Key: k}
	for _, x := range strings.Split(vals, ",") {
		if x = strings.TrimSpace(x); x != "" {
			a.Values = append(a.Values, x)
		}
	}
	return a, nil
}

// matrixFileValues reads an @file's values: one per line, # comments and
// blank lines skipped.
func matrixFileValues(data []byte) []string {
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// IsMatrix reports whether the round is a matrix.
func (i Info) IsMatrix() bool { return len(i.Matrix) > 0 || len(i.Subs) > 0 }

// Cells expands the matrix into its cells (nil for a plain lane): the cross
// product of the axes, minus excluded combinations, at most MaxCells.
func (i Info) Cells() []Cell {
	if !i.IsMatrix() {
		return nil
	}
	if i.IsGroup() {
		out := make([]Cell, len(i.Subs))
		for k, s := range i.Subs {
			out[k] = Cell{Name: s.Name}
		}
		return out
	}
	combos := [][]KV{{}}
	for _, a := range i.Matrix {
		var next [][]KV
		for _, c := range combos {
			for _, v := range a.Values {
				next = append(next, append(append([]KV(nil), c...), KV{a.Key, v}))
			}
		}
		combos = next
	}
	var out []Cell
	for _, c := range combos {
		if excluded(c, i.MatrixExclude) {
			continue
		}
		name := c[0].V
		if len(c) > 1 {
			parts := make([]string, len(c))
			for k, kv := range c {
				parts[k] = kv.K + "=" + kv.V
			}
			name = strings.Join(parts, ",")
		}
		out = append(out, Cell{Name: name, Vars: c})
		if len(out) == MaxCells {
			break
		}
	}
	return out
}

func excluded(c []KV, rules [][]KV) bool {
	for _, rule := range rules {
		all := len(rule) > 0
		for _, want := range rule {
			hit := false
			for _, kv := range c {
				hit = hit || kv == want
			}
			all = all && hit
		}
		if all {
			return true
		}
	}
	return false
}

// Cell finds a cell by name (or, for a cross product, by a unique part of
// it: "orders" for ENV=prod,SVC=orders when only one cell has it).
func (i Info) Cell(name string) (Cell, bool) {
	var partial []Cell
	for _, c := range i.Cells() {
		if c.Name == name {
			return c, true
		}
		for _, kv := range c.Vars {
			if kv.V == name || kv.K+"="+kv.V == name {
				partial = append(partial, c)
				break
			}
		}
	}
	if len(partial) == 1 {
		return partial[0], true
	}
	return Cell{}, false
}

// ForCell is the round as cell c runs it: $KEY (or ${KEY}) in its Round,
// Locks and Outputs replaced by the cell's values, and its job <job>/<cell>.
func (i Info) ForCell(c Cell) Info {
	if i.IsGroup() {
		// A sub-job: its own header, in the lane's job unless it names one.
		for _, s := range i.Subs {
			if s.Name != c.Name {
				continue
			}
			out := s.Info
			switch {
			case out.Job != "":
			case i.Job != "":
				out.Job = i.Job + "/" + c.Name
			case os.Getenv(MatrixJobEnv) != "":
				out.Job = os.Getenv(MatrixJobEnv) + "/" + c.Name
			}
			return out
		}
		return i
	}
	sub := func(s string) string {
		for _, kv := range c.Vars {
			s = strings.ReplaceAll(s, "${"+kv.K+"}", kv.V)
			s = strings.ReplaceAll(s, "$"+kv.K, kv.V)
		}
		return s
	}
	out := i
	out.Round = sub(i.Round)
	out.Locks = nil
	for _, l := range i.Locks {
		out.Locks = append(out.Locks, sub(l))
	}
	out.Outputs = nil
	for _, o := range i.Outputs {
		out.Outputs = append(out.Outputs, sub(o))
	}
	switch {
	case i.Job != "":
		out.Job = i.Job + "/" + c.Name
	case os.Getenv(MatrixJobEnv) != "":
		// No Job: line: the launcher made one job for the round.
		out.Job = os.Getenv(MatrixJobEnv) + "/" + c.Name
	}
	return out
}

// CellLocks reports whether a lock name varies per cell ($KEY in it).
func CellLocks(locks []string) (perCell, perLane []string) {
	for _, l := range locks {
		if strings.Contains(l, "$") {
			perCell = append(perCell, l)
		} else {
			perLane = append(perLane, l)
		}
	}
	return perCell, perLane
}

// setMatrixLine reads one Matrix-family header value.
func (i *Info) setMatrixLine(key, v string) {
	switch key {
	case "Matrix":
		a, err := parseAxis(v)
		if err != nil {
			i.Problems = append(i.Problems, err.Error())
			return
		}
		for _, have := range i.Matrix {
			if have.Key == a.Key {
				i.Problems = append(i.Problems, fmt.Sprintf("Matrix key %s given twice; the first wins", a.Key))
				return
			}
		}
		i.Matrix = append(i.Matrix, a)
	case "Matrix-Exclude":
		var rule []KV
		for _, part := range strings.Split(v, ",") {
			k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				i.Problems = append(i.Problems, fmt.Sprintf("Matrix-Exclude %q: write KEY=value,KEY=value", v))
				return
			}
			rule = append(rule, KV{strings.TrimSpace(k), strings.TrimSpace(val)})
		}
		i.MatrixExclude = append(i.MatrixExclude, rule)
	case "Matrix-Parallel":
		var n int
		if _, err := fmt.Sscan(v, &n); err != nil || n < 1 {
			i.Problems = append(i.Problems, fmt.Sprintf("Matrix-Parallel %q is not a number of cells (1 or more); 1 applies", v))
			return
		}
		i.MatrixParallel = n
	case "Matrix-Fail":
		switch v = strings.ToLower(strings.TrimSpace(v)); v {
		case "continue", "stop":
			i.MatrixFail = v
		default:
			i.Problems = append(i.Problems, fmt.Sprintf("Matrix-Fail %q is not continue or stop; continue applies", v))
		}
	}
}

// resolveMatrix reads @file axes (relative to root) and checks the cells.
func (i *Info) resolveMatrix(root string) {
	for k, a := range i.Matrix {
		if a.File == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, a.File))
		if err != nil {
			i.Problems = append(i.Problems, fmt.Sprintf("Matrix %s=@%s: %v", a.Key, a.File, err))
			continue
		}
		i.Matrix[k].Values = matrixFileValues(data)
	}
	total := 1
	for _, a := range i.Matrix {
		if len(a.Values) == 0 {
			i.Problems = append(i.Problems, fmt.Sprintf("Matrix %s has no values", a.Key))
		}
		for _, v := range a.Values {
			if !cellValue(v) {
				i.Problems = append(i.Problems, fmt.Sprintf("Matrix %s value %q: use letters, digits and . _ : / @ + -", a.Key, v))
			}
		}
		total *= max(len(a.Values), 1)
	}
	if total > MaxCells {
		i.Problems = append(i.Problems, fmt.Sprintf("the matrix has %d cells; only the first %d run", total, MaxCells))
	}
}

// ScriptBytes is what a round's script hash covers: the script, plus the
// files its matrix reads values from (# Matrix: KEY=@file), so approvals
// and queue pins cover the cell list too.
func ScriptBytes(root string, n int) ([]byte, error) {
	path, files, err := ScriptParts(root, n)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil || IsBinary(data) {
		return data, err
	}
	return JoinScript(data, files, func(f string) ([]byte, error) { return os.ReadFile(filepath.Join(root, f)) }), nil
}

// ScriptParts are the files ScriptBytes hashes: the script's path (a lane
// of jobs is its file) and, sorted, the repo-relative files it adds: matrix
// @files, # Cache: lockfiles and # Includes: matches. A compiled lane adds none.
func ScriptParts(root string, n int) (string, []string, error) {
	path := Script(root, n)
	if _, ok := LaneJobs(root, n); ok {
		path = Scripts(root, n)[0] // a lane of jobs hashes as its file, from any job
	}
	data, err := os.ReadFile(path)
	if err != nil || IsBinary(data) {
		return path, nil, err
	}
	info := ParseScript(string(data))
	var files []string
	for _, a := range info.Matrix {
		if a.File != "" {
			files = append(files, a.File)
		}
	}
	if sc := Sidecar(root, n); fileExists(sc) {
		var side Info
		if applySidecar(sc, &side) == nil {
			for _, a := range side.Matrix {
				if a.File != "" {
					files = append(files, a.File)
				}
			}
		}
	}
	// # Cache: lockfiles: what the tool checks the cache against, so the
	// approval covers what it may take from the cache.
	files = append(files, cache.Files(info.Cache)...)
	if sc := Sidecar(root, n); fileExists(sc) {
		var side Info
		if applySidecar(sc, &side) == nil {
			files = append(files, cache.Files(side.Cache)...)
		}
	}
	// # Includes: (globs): local modules and other files that are part of
	// the round.
	for _, g := range append(info.Includes, sideIncludes(root, n)...) {
		matches, _ := filepath.Glob(filepath.Join(root, g))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				if rel, err := filepath.Rel(root, m); err == nil {
					files = append(files, filepath.ToSlash(rel))
				}
			}
		}
	}
	sort.Strings(files)
	return path, dedupe(files), nil
}

// JoinScript is ScriptBytes from the script's data and its files, read
// with read (from the repo, or from a run's staged copy).
func JoinScript(data []byte, files []string, read func(string) ([]byte, error)) []byte {
	var b bytes.Buffer
	b.Write(data)
	for _, f := range files {
		fd, err := read(f)
		if err != nil {
			fd = []byte("(missing)\n")
		}
		fmt.Fprintf(&b, "\n# swim: included file %s\n", f)
		b.Write(fd)
	}
	return b.Bytes()
}

func sideIncludes(root string, n int) []string {
	sc := Sidecar(root, n)
	if !fileExists(sc) {
		return nil
	}
	var side Info
	if applySidecar(sc, &side) != nil {
		return nil
	}
	return side.Includes
}

func dedupe(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
