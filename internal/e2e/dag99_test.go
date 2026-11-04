package e2e

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// dag99 is a 99-lane DAG: lanes 1-9 do real work over fixture pages
// (e2e/scenarios/dag99/lanes), lanes 10-98 are generated nodes whose value
// is a hash of their parents' values, and lane 99 audits the run (every
// node done, every edge honoured, every value recomputed). The lanes' work
// is e2etool's (e2e/cmd/e2etool).

// dagNode is one generated lane (10..98).
type dagNode struct {
	n     int
	kind  string
	after []int
}

// dagTopology is fixed so the graph (and the expected counts: 197 edges, 89
// values, roots 1 8 20 21 22) never changes between runs or machines:
//
//	10..19  fan-out: ten children of lane 9
//	20..22  independent roots (no parents)
//	23..37  deep chain, 15 lanes
//	38      wide fan-in: 13 parents (10..22)
//	39..98  random layers: 1-4 lower-numbered parents each
//	99      audit: waits on every sink
var dagTopology = []dagNode{
	{10, "fan-out of 9", []int{9}}, {11, "fan-out of 9", []int{9}}, {12, "fan-out of 9", []int{9}},
	{13, "fan-out of 9", []int{9}}, {14, "fan-out of 9", []int{9}}, {15, "fan-out of 9", []int{9}},
	{16, "fan-out of 9", []int{9}}, {17, "fan-out of 9", []int{9}}, {18, "fan-out of 9", []int{9}},
	{19, "fan-out of 9", []int{9}},
	{20, "independent root", nil}, {21, "independent root", nil}, {22, "independent root", nil},
	{23, "chain head", []int{19}},
	{24, "chain", []int{23}}, {25, "chain", []int{24}}, {26, "chain", []int{25}}, {27, "chain", []int{26}},
	{28, "chain", []int{27}}, {29, "chain", []int{28}}, {30, "chain", []int{29}}, {31, "chain", []int{30}},
	{32, "chain", []int{31}}, {33, "chain", []int{32}}, {34, "chain", []int{33}}, {35, "chain", []int{34}},
	{36, "chain", []int{35}}, {37, "chain", []int{36}},
	{38, "wide fan-in (13 parents)", []int{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22}},
	{39, "random", []int{18, 34}}, {40, "random", []int{7, 29, 31}}, {41, "random", []int{26, 30, 32, 36}},
	{42, "random", []int{29, 35}}, {43, "random", []int{12, 37, 38}}, {44, "random", []int{35, 37, 38, 39}},
	{45, "random", []int{42, 43}}, {46, "random", []int{31, 44}}, {47, "random", []int{38, 40, 41, 46}},
	{48, "random", []int{37, 39, 40}}, {49, "random", []int{34, 43, 45}}, {50, "random", []int{36}},
	{51, "random", []int{16}}, {52, "random", []int{34, 40, 49}}, {53, "random", []int{42, 49, 51}},
	{54, "random", []int{42}}, {55, "random", []int{46, 51, 53}}, {56, "random", []int{2, 45, 48}},
	{57, "random", []int{44, 50}}, {58, "random", []int{10}}, {59, "random", []int{52, 53, 55}},
	{60, "random", []int{22}}, {61, "random", []int{43, 58}}, {62, "random", []int{17}},
	{63, "random", []int{35, 60}}, {64, "random", []int{12, 57}}, {65, "random", []int{59}},
	{66, "random", []int{29, 58, 65}}, {67, "random", []int{23, 60}}, {68, "random", []int{59, 61}},
	{69, "random", []int{61, 64}}, {70, "random", []int{27}}, {71, "random", []int{56}},
	{72, "random", []int{17, 62, 65, 68}}, {73, "random", []int{29, 71}}, {74, "random", []int{66, 69, 71}},
	{75, "random", []int{5, 71}}, {76, "random", []int{25, 72}}, {77, "random", []int{75}},
	{78, "random", []int{48}}, {79, "random", []int{40, 78}}, {80, "random", []int{72, 74, 75, 77}},
	{81, "random", []int{60}}, {82, "random", []int{68}}, {83, "random", []int{23, 71, 74, 79}},
	{84, "random", []int{79, 81, 82}}, {85, "random", []int{66}}, {86, "random", []int{53, 77}},
	{87, "random", []int{27, 86}}, {88, "random", []int{58, 84}}, {89, "random", []int{32, 81, 82, 85}},
	{90, "random", []int{10}}, {91, "random", []int{56, 84}}, {92, "random", []int{14, 82, 86}},
	{93, "random", []int{63, 83, 89, 90}}, {94, "random", []int{38, 86, 93}}, {95, "random", []int{41}},
	{96, "random", []int{38}}, {97, "random", []int{20}}, {98, "random", []int{41, 87, 94, 96}},
}

// dagLockPool are the locks dag99-locks hands out.
var dagLockPool = []string{"pool/a", "pool/b", "pool/c", "pool/d", "pool/e"}

// dagLocks is lane n's # Locks: in dag99-locks, by lane number (fixed, so
// runs are comparable): n%7 = 0 none, 1-5 one lock, 6 two.
func dagLocks(n int) string {
	switch k := n % 7; {
	case k == 0:
		return ""
	case k <= 5:
		return dagLockPool[k-1]
	default:
		return dagLockPool[0] + ", " + dagLockPool[2]
	}
}

// dag99 writes the 99 lanes into a scenario repo, with locks on lanes
// 10-98 if asked, and requires them to lint clean (warnings too): that
// keeps them honest and swim lint exercised on 99 real scripts.
func dag99(t *testing.T, r *repo, locks bool) {
	t.Helper()
	r.lanesFrom("dag99")
	for _, nd := range dagTopology {
		l := ""
		if locks {
			l = dagLocks(nd.n)
		}
		r.write(fmt.Sprintf("lane.%d.sh", nd.n), dagNodeLane(nd, l), 0o644)
	}
	r.write("lane.99.sh", dagAuditLane(dagSinks(t, r)), 0o644)
	if out, code := r.swim("lint", "--strict"); code != 0 {
		t.Fatalf("swim lint --strict failed on the dag99 lanes:\n%s", out)
	}
}

// dagNodeLane is a generated lane: it checks its parents finished, in this
// run, before it started, then writes its value, sha256 over theirs.
func dagNodeLane(nd dagNode, locks string) string {
	ps := joinInts(nd.after, " ")
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n# swim: syntax 2\n")
	fmt.Fprintf(&b, "# Round: DAG99 node %d (%s): after %s\n", nd.n, nd.kind, orNothing(ps))
	if locks != "" {
		fmt.Fprintf(&b, "# Locks: %s\n", locks)
	}
	fmt.Fprintf(&b, "# Job:   %s\n# After: %s\n", newUUID(), ps)
	b.WriteString("#\n# Synthetic dag99 node: no network, nothing destructive. Its value is\n" +
		"# sha256 over its parents' values, so lane 99 can recompute the DAG.\n" +
		"# E2E_FAIL=\"3 41\" fails those lanes on purpose.\n\n")
	b.WriteString(`_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
`)
	fmt.Fprintf(&b, "lane_init %d\n\nD=$SWIM_ROOT/.scenario/dag\nN=%d\nPARENTS=%q\n", nd.n, nd.n, ps)
	b.WriteString(`NODE_START=$(e2etool now)
RUN_ID=$SWIM_RUN
export D N PARENTS NODE_START RUN_ID
mkdir -p "$D/nodes"
run "clear own marker and value" rm -f "$D/nodes/$N.done" "$D/nodes/$N.val"
stage check
`)
	if ps != "" {
		b.WriteString(`gate "parents finished before this node started (swim $PARENTS)" e2etool parents-check $PARENTS` + "\n")
	}
	b.WriteString(`
stage change
run "simulated work" true
gate "value from parents -> nodes/$N.val" e2etool node-value $PARENTS

stage verify
gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(e2etool now)" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
`)
	return b.String()
}

// dagAuditLane is lane 99: it waits on every sink, so it runs last, and
// audits the run (e2etool audit).
func dagAuditLane(sinks []int) string {
	ps := joinInts(sinks, " ")
	return `#!/usr/bin/env bash
# swim: syntax 2
# Round: DAG99 audit: every node done, every edge honoured, every value matches
# Job:   ` + newUUID() + `
# After: ` + ps + `
#
# Reads every lane's # After: line, nodes/N.done and N.val, and .swim.log.
# Checks every node finished in this run, every edge finished-before-started,
# every value recomputes from the graph, and the roots started together.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 99

D=$SWIM_ROOT/.scenario/dag
N=99
PARENTS="` + ps + `"
NODE_START=$(e2etool now)
RUN_ID=$SWIM_RUN
export D N PARENTS NODE_START RUN_ID
mkdir -p "$D/nodes"
run "clear own marker" rm -f "$D/nodes/$N.done"
stage check
gate "parents finished before this node started (swim $PARENTS)" e2etool parents-check $PARENTS

stage verify
gate "audit the DAG" e2etool audit

summary
`
}

var afterLine = regexp.MustCompile(`(?m)^# After:[ \t]*(.*)$`)

// laneGraph reads every lane.N.sh in r: lane -> its # After: parents.
func laneGraph(t *testing.T, r *repo) map[int][]int {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(r.root, "lane.*.sh"))
	g := map[int][]int{}
	for _, p := range paths {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "lane."), ".sh"))
		if err != nil {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		g[n] = []int{}
		if m := afterLine.FindSubmatch(src); m != nil {
			for _, f := range strings.Fields(string(m[1])) {
				k, err := strconv.Atoi(f)
				if err != nil {
					t.Fatalf("%s: bad # After: entry %q", p, f)
				}
				g[n] = append(g[n], k)
			}
		}
	}
	return g
}

// dagSinks are lanes 1..98 nobody depends on.
func dagSinks(t *testing.T, r *repo) []int {
	hasChild := map[int]bool{}
	g := laneGraph(t, r)
	for _, ps := range g {
		for _, p := range ps {
			hasChild[p] = true
		}
	}
	var sinks []int
	for n := range g {
		if n < 99 && !hasChild[n] {
			sinks = append(sinks, n)
		}
	}
	sort.Ints(sinks)
	return sinks
}

// descendants are the lanes downstream of roots, ascending.
func descendants(t *testing.T, r *repo, roots ...int) []int {
	kids := map[int][]int{}
	for n, ps := range laneGraph(t, r) {
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
	return out
}

func orNothing(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

// newUUID is a random version 4 UUID: every generated lane gets a fresh
// job, so every scenario is a new set of rounds.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
