package lane

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MaxLanes is the highest lane number.
const MaxLanes = 99

// A repo has no fixed number of lanes: its lanes are the ones in use, found
// on disk (Associated). A lane is in use while it has
//
//   - a script that isn't a stub (lane.N.sh, lane.N.py, ..., or a group
//     file's lane),
//   - a current log (.swim/logs/agentN.log, or a job's or cell's,
//     agentN.<job>.log), or
//   - a process running it.
//
// So `swim new 7` adds lane 7, and `swim archive 7` (which moves its logs
// aside) with a stub or no script drops it again. Archived logs
// (agentN.prev-*.log) and check logs don't count.

// laneOfFile reads the lane number from a file named prefix N[.more]
// suffix: a lane's current log, or a job's or matrix cell's (agent3.log,
// agent1.2.log, agent3.orders.log), or a pid file (lane3.pid,
// lane3.orders.pid).
func laneOfFile(name, prefix, suffix string) (int, bool) {
	mid, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return 0, false
	}
	if mid, ok = strings.CutSuffix(mid, suffix); !ok {
		return 0, false
	}
	num, more, dotted := strings.Cut(mid, ".")
	if !isDigits(num) || dotted && (more == "" || strings.Contains(more, "/")) {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil
}

// Associated lists the lanes root is using, in order (see above).
func Associated(root string) []int {
	in := map[int]bool{}
	add := func(n int) {
		if n >= 1 && n <= MaxLanes {
			in[n] = true
		}
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, _, ok := ParseScriptName(e.Name()); ok && !in[n] && !isStubFile(filepath.Join(root, e.Name())) {
			add(n)
		}
	}
	logs, _ := os.ReadDir(LogDir(root))
	for _, e := range logs {
		name := e.Name()
		if strings.Contains(name, ".prev-") || strings.HasSuffix(name, ".check.log") {
			continue
		}
		if n, ok := laneOfFile(name, "agent", ".log"); ok {
			add(n)
		}
	}
	pids, _ := os.ReadDir(filepath.Join(root, ".swim"))
	for _, e := range pids {
		if n, ok := laneOfFile(e.Name(), "lane", ".pid"); ok {
			if _, _, ok := runningIn(filepath.Join(root, ".swim", e.Name())); ok {
				add(n)
			}
		}
	}
	out := make([]int, 0, len(in))
	for n := range in {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// isStubFile reports whether a lane script is a stub (or not a runnable
// lane script at all: lane.1.notes), cheaply: config loads call this for
// every lane, in every swim process.
func isStubFile(path string) bool {
	k, ext, _ := ParseScriptName(filepath.Base(path))
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || k == 0 {
		return true
	}
	if ext != "" && !RunnerExts[ext] && st.Mode()&0o111 == 0 {
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	head := make([]byte, 16<<10)
	n, _ := f.Read(head)
	return bytes.Contains(head[:n], []byte(StubMarker))
}
