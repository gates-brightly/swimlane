package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// node is one generated dag99 lane (10..98).
type node struct {
	n     int
	kind  string
	after []int
}

// topology is the dag99 graph for lanes 10..98. It is fixed so the graph
// (and the scenario's expected counts: 197 edges, 89 values, roots 1 8 20 21
// 22) never changes between runs or machines:
//
//	10..19  fan-out: ten children of lane 9
//	20..22  independent roots (no parents)
//	23..37  deep chain, 15 lanes
//	38      wide fan-in: 13 parents (10..22)
//	39..98  random layers: 1-4 lower-numbered parents each
//	99      audit: waits on every sink (computed from the lane scripts)
var topology = []node{
	{10, "fan-out of 9", []int{9}},
	{11, "fan-out of 9", []int{9}},
	{12, "fan-out of 9", []int{9}},
	{13, "fan-out of 9", []int{9}},
	{14, "fan-out of 9", []int{9}},
	{15, "fan-out of 9", []int{9}},
	{16, "fan-out of 9", []int{9}},
	{17, "fan-out of 9", []int{9}},
	{18, "fan-out of 9", []int{9}},
	{19, "fan-out of 9", []int{9}},
	{20, "independent root", []int{}},
	{21, "independent root", []int{}},
	{22, "independent root", []int{}},
	{23, "chain head", []int{19}},
	{24, "chain", []int{23}},
	{25, "chain", []int{24}},
	{26, "chain", []int{25}},
	{27, "chain", []int{26}},
	{28, "chain", []int{27}},
	{29, "chain", []int{28}},
	{30, "chain", []int{29}},
	{31, "chain", []int{30}},
	{32, "chain", []int{31}},
	{33, "chain", []int{32}},
	{34, "chain", []int{33}},
	{35, "chain", []int{34}},
	{36, "chain", []int{35}},
	{37, "chain", []int{36}},
	{38, "wide fan-in (13 parents)", []int{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22}},
	{39, "random", []int{18, 34}},
	{40, "random", []int{7, 29, 31}},
	{41, "random", []int{26, 30, 32, 36}},
	{42, "random", []int{29, 35}},
	{43, "random", []int{12, 37, 38}},
	{44, "random", []int{35, 37, 38, 39}},
	{45, "random", []int{42, 43}},
	{46, "random", []int{31, 44}},
	{47, "random", []int{38, 40, 41, 46}},
	{48, "random", []int{37, 39, 40}},
	{49, "random", []int{34, 43, 45}},
	{50, "random", []int{36}},
	{51, "random", []int{16}},
	{52, "random", []int{34, 40, 49}},
	{53, "random", []int{42, 49, 51}},
	{54, "random", []int{42}},
	{55, "random", []int{46, 51, 53}},
	{56, "random", []int{2, 45, 48}},
	{57, "random", []int{44, 50}},
	{58, "random", []int{10}},
	{59, "random", []int{52, 53, 55}},
	{60, "random", []int{22}},
	{61, "random", []int{43, 58}},
	{62, "random", []int{17}},
	{63, "random", []int{35, 60}},
	{64, "random", []int{12, 57}},
	{65, "random", []int{59}},
	{66, "random", []int{29, 58, 65}},
	{67, "random", []int{23, 60}},
	{68, "random", []int{59, 61}},
	{69, "random", []int{61, 64}},
	{70, "random", []int{27}},
	{71, "random", []int{56}},
	{72, "random", []int{17, 62, 65, 68}},
	{73, "random", []int{29, 71}},
	{74, "random", []int{66, 69, 71}},
	{75, "random", []int{5, 71}},
	{76, "random", []int{25, 72}},
	{77, "random", []int{75}},
	{78, "random", []int{48}},
	{79, "random", []int{40, 78}},
	{80, "random", []int{72, 74, 75, 77}},
	{81, "random", []int{60}},
	{82, "random", []int{68}},
	{83, "random", []int{23, 71, 74, 79}},
	{84, "random", []int{79, 81, 82}},
	{85, "random", []int{66}},
	{86, "random", []int{53, 77}},
	{87, "random", []int{27, 86}},
	{88, "random", []int{58, 84}},
	{89, "random", []int{32, 81, 82, 85}},
	{90, "random", []int{10}},
	{91, "random", []int{56, 84}},
	{92, "random", []int{14, 82, 86}},
	{93, "random", []int{63, 83, 89, 90}},
	{94, "random", []int{38, 86, 93}},
	{95, "random", []int{41}},
	{96, "random", []int{38}},
	{97, "random", []int{20}},
	{98, "random", []int{41, 87, 94, 96}},
}

// cmdGen writes lanes 10..99 of the dag99 scenario into the cwd from the
// templates in SCENARIO_DIR/parts. Lanes 1..9 must already be in place: their
// # After: lines decide which lanes are sinks for the audit to wait on. Every
// generated lane gets a fresh job id, so every run is a new set of rounds.
func cmdGen(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: e2etool gen SCENARIO_DIR")
	}
	part := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(args[0], "parts", name))
		return string(b), err
	}
	tmpl, err := part("node.tmpl")
	if err != nil {
		return err
	}
	pcheck, err := part("parent_check.sh")
	if err != nil {
		return err
	}
	derive, err := part("derive.sh")
	if err != nil {
		return err
	}
	audit, err := part("audit.sh")
	if err != nil {
		return err
	}

	for _, nd := range topology {
		ps := joinInts(nd.after, " ")
		round := ps
		if round == "" {
			round = "nothing"
		}
		parentsTxt, check, runID := "none", "", "RUN_ID=$SWIM_RUN\n"
		if ps != "" {
			parentsTxt, check, runID = "swim "+ps, pcheck, derive
		}
		s := strings.NewReplacer(
			"@ROUND@", fmt.Sprintf("DAG99 node %d (%s): after %s", nd.n, nd.kind, round),
			"@JOB@", uuid(),
			"@AFTER@", ps,
			"@N@", strconv.Itoa(nd.n),
			"@KIND@", nd.kind,
			"@PARENTS_TXT@", parentsTxt,
			"@PARENT_CHECK@", check,
			"@RUN_ID@", runID,
		).Replace(tmpl)
		if err := os.WriteFile(fmt.Sprintf("lane.%d.sh", nd.n), []byte(s), 0o644); err != nil {
			return err
		}
	}

	// Lane 99 waits on every sink: lanes 1..98 nobody depends on.
	after, err := graph()
	if err != nil {
		return err
	}
	hasChild := map[int]bool{}
	for _, ps := range after {
		for _, p := range ps {
			hasChild[p] = true
		}
	}
	var sinks []int
	for n := 1; n < 99; n++ {
		if _, ok := after[n]; ok && !hasChild[n] {
			sinks = append(sinks, n)
		}
	}
	s := strings.NewReplacer(
		"@JOB@", uuid(),
		"@SINKS@", joinInts(sinks, " "),
		"@PARENT_CHECK@", pcheck,
		"@DERIVE@", derive,
		"@AUDIT@", audit,
	).Replace(auditLane)
	return os.WriteFile("lane.99.sh", []byte(s), 0o644)
}

// uuid returns a random version 4 UUID.
func uuid() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const auditLane = `#!/usr/bin/env bash
# swim: syntax 2
# Round: DAG99 audit: every node done, every edge honoured, every value matches
# Job:   @JOB@
# After: @SINKS@
# Lane:  swim 99
#
# Goal:
#   Audit the 99-lane DAG. Waits on every sink, so it runs last.
#   Reads every lane's ` + "`# After:`" + ` line, nodes/N.done and N.val, and .swim.log
#   (which lanes were in this swim run). Checks: all nodes done on the right
#   run id; every edge finished-before-started; every synthetic value
#   recomputes from the graph (data flow, not just timing); independent roots
#   started together. Reports scheduler latency and makespan vs critical path.
#
# Steps:
#   0. Clear own marker; gate: every sink finished, for this run, before this started
#   1. Gate: audit the DAG (prints a timeline of this run)
#
# Guard flags this round honours (flag: action, date, reason):
#   (none)
#
# Part of the e2e scenario e2e/scenarios/dag99 (run: e2e/run.sh dag99).
# Do not use ` + "`set -e`" + `: failed checks keep going; only gates stop the round.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 99

D=$SWIM_ROOT/.scenario/dag
N=99
PARENTS="@SINKS@"
NODE_START=$(e2etool now)
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own marker" rm -f "$D/nodes/$N.done"
stage check
@PARENT_CHECK@@DERIVE@export RUN_ID

stage verify
@AUDIT@
summary
`
