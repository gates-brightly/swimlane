package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// h is the scenario's value hash: the first 16 hex digits of sha256.
func h(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func cmdNow([]string) error {
	fmt.Printf("%.3f\n", now())
	return nil
}

var (
	afterRE = regexp.MustCompile(`(?m)^# After:[ \t]*(.*)$`)
	stubRE  = regexp.MustCompile(`(?m)^# swim:stub`)
	laneRE  = regexp.MustCompile(`^lane\.(\d+)\.sh$`)
)

// graph reads every non-stub lane.N.sh in the cwd: lane -> its # After: parents.
func graph() (map[int][]int, error) {
	paths, err := filepath.Glob("lane.*.sh")
	if err != nil {
		return nil, err
	}
	after := map[int][]int{}
	for _, p := range paths {
		m := laneRE.FindStringSubmatch(filepath.Base(p))
		if m == nil {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if stubRE.Match(src) {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		ps := []int{}
		if a := afterRE.FindSubmatch(src); a != nil {
			for _, f := range strings.Fields(string(a[1])) {
				k, err := strconv.Atoi(f)
				if err != nil {
					return nil, fmt.Errorf("%s: bad # After: entry %q", p, f)
				}
				ps = append(ps, k)
			}
		}
		after[n] = ps
	}
	return after, nil
}

func sortedKeys(m map[int][]int) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}

func joinInts(ns []int, sep string) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, sep)
}

func atoiAll(args []string) ([]int, error) {
	out := make([]int, len(args))
	for i, a := range args {
		n, err := strconv.Atoi(a)
		if err != nil {
			return nil, fmt.Errorf("%q is not a lane number", a)
		}
		out[i] = n
	}
	return out, nil
}

func cmdDescendants(args []string) error {
	roots, err := atoiAll(args)
	if err != nil {
		return err
	}
	after, err := graph()
	if err != nil {
		return err
	}
	kids := map[int][]int{}
	for n, ps := range after {
		for _, p := range ps {
			kids[p] = append(kids[p], n)
		}
	}
	seen := map[int]bool{}
	stack := append([]int(nil), roots...)
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range kids[top] {
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	var out []int
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	fmt.Println(joinInts(out, " "))
	return nil
}

func cmdEdges([]string) error {
	after, err := graph()
	if err != nil {
		return err
	}
	total := 0
	for _, ps := range after {
		total += len(ps)
	}
	fmt.Println(total)
	return nil
}

// done is a parsed nodes/N.done marker: "<run> <start> <end>".
type done struct {
	run        string
	start, end float64
	ended      bool // false for the auditing lane itself (still running)
}

func readDone(d string, n int) (done, error) {
	data, err := os.ReadFile(filepath.Join(d, "nodes", fmt.Sprintf("%d.done", n)))
	if err != nil {
		return done{}, err
	}
	f := strings.Fields(string(data))
	if len(f) != 3 {
		return done{}, fmt.Errorf("nodes/%d.done: want 3 fields, got %q", n, strings.TrimSpace(string(data)))
	}
	s, err1 := strconv.ParseFloat(f[1], 64)
	e, err2 := strconv.ParseFloat(f[2], 64)
	if err1 != nil || err2 != nil {
		return done{}, fmt.Errorf("nodes/%d.done: bad times", n)
	}
	return done{run: f[0], start: s, end: e, ended: true}, nil
}

func nodeStart() (float64, error) {
	s, err := env("NODE_START")
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

// runLanes returns the lanes in this swim run, from SWIM_RUN_LANES.
func runLanes() map[int]bool {
	set := map[int]bool{}
	for _, f := range strings.Fields(os.Getenv("SWIM_RUN_LANES")) {
		if n, err := strconv.Atoi(f); err == nil {
			set[n] = true
		}
	}
	return set
}

// parents-check P...: every parent in this swim run finished, on this run
// (SWIM_RUN), before this node started. A parent outside this run passed in
// an earlier one (swim only runs a lane whose outside dependencies passed),
// so its marker just has to exist.
func cmdParentsCheck(args []string) error {
	parents, err := atoiAll(args)
	if err != nil {
		return err
	}
	d, err := env("D")
	if err != nil {
		return err
	}
	run, err := env("SWIM_RUN")
	if err != nil {
		return err
	}
	start, err := nodeStart()
	if err != nil {
		return err
	}
	inRun := runLanes()
	ok := true
	for _, p := range parents {
		nd, err := readDone(d, p)
		if err != nil {
			fmt.Printf("swim %d: no done marker  BAD\n", p)
			ok = false
			continue
		}
		gap := start - nd.end
		verdict := "ok"
		switch {
		case !inRun[p]:
			verdict = "ok (earlier run)"
		case nd.run != run:
			verdict, ok = "BAD (stale run)", false
		case gap < 0:
			verdict, ok = "BAD (overlap)", false
		}
		fmt.Printf("swim %d: run=%s  finished %6.2fs before this node started  %s\n", p, nd.run, gap, verdict)
	}
	if !ok {
		return exitCode(1)
	}
	return nil
}

func prefix(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// node-value P...: the node's value is sha256 over its parents' values, so
// the audit can recompute the whole DAG and catch a node that read early.
func cmdNodeValue(args []string) error {
	parents, err := atoiAll(args)
	if err != nil {
		return err
	}
	d, err := env("D")
	if err != nil {
		return err
	}
	n, err := env("N")
	if err != nil {
		return err
	}
	sort.Ints(parents)
	var vals []string
	for _, p := range parents {
		if v, err := os.ReadFile(filepath.Join(d, "nodes", fmt.Sprintf("%d.val", p))); err == nil {
			vals = append(vals, strings.TrimSpace(string(v)))
			continue
		}
		nd, err := readDone(d, p) // a real-work lane carries no value: hash its run id
		if err != nil {
			return err
		}
		vals = append(vals, h(fmt.Sprintf("%d:%s", p, nd.run)))
	}
	val := h(n + ":root")
	if len(vals) > 0 {
		val = h(n + "|" + strings.Join(vals, "|"))
	}
	dst := filepath.Join(d, "nodes", n+".val")
	if err := writeAtomic(dst, filepath.Join(d, "nodes", "."+n+".val"), []byte(val+"\n")); err != nil {
		return err
	}
	ps := joinInts(parents, " ")
	if ps == "" {
		ps = "-"
	}
	fmt.Printf("parents=%s value=%s\n", ps, val)
	return nil
}

// thisRun returns the lanes in this swim run (SWIM_RUN_LANES), or every
// lane if it isn't set.
func thisRun(after map[int][]int) map[int]bool {
	if set := runLanes(); len(set) > 0 {
		return set
	}
	set := map[int]bool{}
	for n := range after {
		set[n] = true
	}
	return set
}

// pyList renders ints like a Python list: [1, 8, 20].
func pyList(ns []int) string { return "[" + joinInts(ns, ", ") + "]" }

// audit: lane 99's checks over the whole DAG.
func cmdAudit([]string) error {
	d, err := env("D")
	if err != nil {
		return err
	}
	meS, err := env("N")
	if err != nil {
		return err
	}
	me, _ := strconv.Atoi(meS)
	start, err := nodeStart()
	if err != nil {
		return err
	}
	run, err := env("SWIM_RUN")
	if err != nil {
		return err
	}
	after, err := graph()
	if err != nil {
		return err
	}
	lanes := sortedKeys(after)
	cur := thisRun(after)
	inRun := 0
	var earlier []int
	for _, n := range lanes {
		if cur[n] {
			inRun++
		} else {
			earlier = append(earlier, n)
		}
	}
	e := joinInts(earlier, " ")
	if e == "" {
		e = "-"
	}
	fmt.Printf("lanes: %d  in this swim run: %d  from an earlier run: %s\n", len(after), inRun, e)

	nodes := map[int]done{}
	for _, n := range lanes {
		if n == me {
			nodes[n] = done{run: run, start: start}
			continue
		}
		if nd, err := readDone(d, n); err == nil {
			nodes[n] = nd
		}
	}
	var bad []string

	// 1. Every node finished. Lanes in this swim run carry its SWIM_RUN;
	//    lanes from an earlier run carry that run's id.
	for _, n := range lanes {
		nd, ok := nodes[n]
		if !ok {
			bad = append(bad, fmt.Sprintf("swim %d: no done marker", n))
			continue
		}
		if cur[n] && nd.run != run {
			bad = append(bad, fmt.Sprintf("swim %d: run %s, expected this run %s", n, nd.run, run))
		}
	}

	// 2. Every edge: the child started after the parent finished.
	edges, ok, cross := 0, 0, 0
	for _, n := range lanes {
		for _, p := range after[n] {
			cn, okc := nodes[n]
			pn, okp := nodes[p]
			if !okc || !okp {
				continue
			}
			edges++
			gap := cn.start - pn.end
			if gap < 0 {
				bad = append(bad, fmt.Sprintf("edge %d->%d violated: child started %.2fs before parent finished", p, n, -gap))
			} else {
				ok++
				if !cur[p] && cur[n] {
					cross++
				}
			}
		}
	}
	fmt.Printf("edges: %d checked, %d ok (%d with the parent from an earlier run), %d violated\n", edges, ok, cross, edges-ok)

	// 3. Data flow: recompute every synthetic node's value from the graph.
	memo := map[int]string{}
	var expect func(n int) (string, error)
	expect = func(n int) (string, error) {
		if v, ok := memo[n]; ok {
			return v, nil
		}
		var v string
		if n < 10 { // real-work lanes carry no value; hash their run id
			nd, ok := nodes[n]
			if !ok {
				return "", fmt.Errorf("%d", n)
			}
			v = h(fmt.Sprintf("%d:%s", n, nd.run))
		} else {
			ps := append([]int(nil), after[n]...)
			sort.Ints(ps)
			if len(ps) == 0 {
				v = h(fmt.Sprintf("%d:root", n))
			} else {
				vs := make([]string, len(ps))
				for i, p := range ps {
					pv, err := expect(p)
					if err != nil {
						return "", err
					}
					vs[i] = pv
				}
				v = h(fmt.Sprintf("%d|%s", n, strings.Join(vs, "|")))
			}
		}
		memo[n] = v
		return v, nil
	}
	vals, vbad := 0, 0
	for _, n := range lanes {
		vf := filepath.Join(d, "nodes", fmt.Sprintf("%d.val", n))
		_, have := nodes[n]
		got, err := os.ReadFile(vf)
		if n < 10 || n >= me || !have || err != nil {
			continue
		}
		vals++
		want, err := expect(n)
		if err != nil {
			bad = append(bad, fmt.Sprintf("swim %d: can't recompute (missing %s)", n, err))
			continue
		}
		if g := strings.TrimSpace(string(got)); g != want {
			vbad++
			bad = append(bad, fmt.Sprintf("swim %d: value %s != expected %s (read a parent early?)", n, g, want))
		}
	}
	fmt.Printf("values: %d recomputed, %d match\n", vals, vals-vbad)

	// 4. Independent roots in this run start together.
	var roots []int
	for _, n := range lanes {
		if _, have := nodes[n]; have && len(after[n]) == 0 && cur[n] {
			roots = append(roots, n)
		}
	}
	if len(roots) > 1 {
		lo, hi := nodes[roots[0]].start, nodes[roots[0]].start
		for _, r := range roots {
			lo, hi = min(lo, nodes[r].start), max(hi, nodes[r].start)
		}
		spread := hi - lo
		verdict := "ok"
		if spread >= 2 {
			verdict = "SERIALISED?"
			bad = append(bad, fmt.Sprintf("independent roots started %.2fs apart in one run", spread))
		}
		fmt.Printf("roots in this run %s: start spread %.2fs  %s\n", pyList(roots), spread, verdict)
	} else {
		r := "-"
		if len(roots) > 0 {
			r = pyList(roots)
		}
		fmt.Printf("roots in this run: %s (need 2+ to test parallel roots)\n", r)
	}

	// 5. Scheduler latency: start - last parent finish, for nodes whose
	//    parents all ran in this run.
	type lat struct {
		x float64
		n int
	}
	var lats []lat
	for _, n := range lanes {
		nd, have := nodes[n]
		if !cur[n] || !have || len(after[n]) == 0 {
			continue
		}
		all, last := true, 0.0
		for i, p := range after[n] {
			pn, okp := nodes[p]
			if !cur[p] || !okp {
				all = false
				break
			}
			if i == 0 || pn.end > last {
				last = pn.end
			}
		}
		if all {
			lats = append(lats, lat{nd.start - last, n})
		}
	}
	sort.Slice(lats, func(a, b int) bool {
		if lats[a].x != lats[b].x {
			return lats[a].x < lats[b].x
		}
		return lats[a].n < lats[b].n
	})
	if len(lats) > 0 {
		fmt.Printf("scheduler latency (start - last parent done), %d nodes: median %.2fs  p95 %.2fs  max %.2fs (swim %d)\n",
			len(lats), lats[len(lats)/2].x, lats[int(float64(len(lats))*.95)].x, lats[len(lats)-1].x, lats[len(lats)-1].n)
	}

	// Concurrency: the most lanes of this run running at the same instant.
	// E2E_MAX_PARALLEL (a scenario's --parallel) makes going over it a failure.
	type edge struct {
		t   float64
		inc int
	}
	var evs []edge
	for n, nd := range nodes {
		if cur[n] && n != me && nd.ended {
			evs = append(evs, edge{nd.start, 1}, edge{nd.end, -1})
		}
	}
	sort.Slice(evs, func(a, b int) bool {
		if evs[a].t != evs[b].t {
			return evs[a].t < evs[b].t
		}
		return evs[a].inc < evs[b].inc // an end before a start at the same instant
	})
	peak, live := 0, 0
	for _, e := range evs {
		live += e.inc
		peak = max(peak, live)
	}
	limit := os.Getenv("E2E_MAX_PARALLEL")
	if limit != "" {
		lim, _ := strconv.Atoi(limit)
		verdict := "ok"
		if peak > lim {
			verdict = "OVER"
			bad = append(bad, fmt.Sprintf("%d lanes ran at once, over the limit of %d", peak, lim))
		}
		fmt.Printf("concurrency: at most %d lanes running at once (limit %d)  %s\n", peak, lim, verdict)
	} else {
		fmt.Printf("concurrency: at most %d lanes running at once\n", peak)
	}

	// Makespan against the critical path by recorded durations.
	endOf := func(nd done) float64 {
		if nd.ended {
			return nd.end
		}
		return nd.start
	}
	first := true
	var t0, end float64
	for n, nd := range nodes {
		if !cur[n] {
			continue
		}
		if first || nd.start < t0 {
			t0 = nd.start
		}
		if first || endOf(nd) > end {
			end = endOf(nd)
		}
		first = false
	}
	cp := map[int]float64{}
	var path func(n int) float64
	path = func(n int) float64 {
		if v, ok := cp[n]; ok {
			return v
		}
		nd := nodes[n]
		best := 0.0
		for _, p := range after[n] {
			if _, okp := nodes[p]; okp && cur[p] {
				best = max(best, path(p))
			}
		}
		cp[n] = endOf(nd) - nd.start + best
		return cp[n]
	}
	crit := 0.0
	for n := range nodes {
		if cur[n] {
			crit = max(crit, path(n))
		}
	}
	fmt.Printf("makespan %.2fs vs critical-path work %.2fs -> scheduling overhead %.2fs\n", end-t0, crit, end-t0-crit)

	// Timeline of this run.
	width := int((end-t0)/.25) + 1
	fmt.Println("\ntimeline of this run (one char = 0.25s)")
	var shown []int
	for n := range nodes {
		shown = append(shown, n)
	}
	sort.Ints(shown)
	for _, n := range shown {
		nd := nodes[n]
		if !cur[n] {
			fmt.Printf("swim %2d   (earlier run)\n", n)
			continue
		}
		e := end
		if nd.ended {
			e = nd.end
		}
		a := int((nd.start - t0) / .25)
		b := max(int((e-t0)/.25), a+1)
		ps := joinInts(after[n], " ")
		if ps == "" {
			ps = "-"
		}
		if len(ps) >= 40 {
			ps = ps[:37] + "..."
		}
		fmt.Printf("swim %2d |%s%s%s| %5.2fs -> %5.2fs  after: %s\n", n,
			strings.Repeat(" ", a), strings.Repeat("#", b-a), strings.Repeat(" ", max(width-b, 0)), nd.start-t0, e-t0, ps)
	}

	if len(bad) == 0 {
		fmt.Println("\nPASS: every node done, every edge honoured, every value matches")
		return nil
	}
	total := len(bad)
	if len(bad) > 40 {
		bad = bad[:40]
	}
	fmt.Printf("\nFAIL (%d):\n  %s\n", total, strings.Join(bad, "\n  "))
	return exitCode(1)
}
