// Command e2etool does the work inside the e2e scenarios' lane scripts:
// the "real work" of dag99 lanes 1-9 (aggregate pages, Markdown, stats,
// links, ...), the per-node checks every generated lane runs, the lane 99
// audit, graph queries for scenario checks, and generating the dag99 lanes.
// e2e/run.sh builds it next to the swim binary under test and puts it on
// PATH, so scenarios need only bash and Go.
//
//	e2etool now                       epoch seconds, 3 decimals
//	e2etool parents-check P...        gate: each parent finished, this run, before NODE_START
//	e2etool run-id P...               this node's run id, from its parents
//	e2etool node-value P...           sha256 value from the parents -> nodes/N.val
//	e2etool audit                     lane 99: every node, edge and value
//	e2etool descendants N...          lanes downstream of N (from lane.*.sh # After:)
//	e2etool edges                     number of # After: edges
//	e2etool gen SCENARIO_DIR          write dag99 lanes 10..99 into the cwd
//	e2etool aggregate|markdown|stats|stats-pages|report|links|wordfreq|reconcile|domains|headers
//	                                  dag99 lanes 1-9's work (see each lane script)
//
// Inputs come from the environment the lane scripts export (D, N, P, IN,
// OUT, H, NODE_START, RUN_ID, SWIM_JOB).
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

var commands = map[string]func(args []string) error{
	"version":       func([]string) error { fmt.Println("e2etool (swim e2e helper)"); return nil },
	"now":           cmdNow,
	"parents-check": cmdParentsCheck,
	"run-id":        cmdRunID,
	"node-value":    cmdNodeValue,
	"audit":         cmdAudit,
	"descendants":   cmdDescendants,
	"edges":         cmdEdges,
	"gen":           cmdGen,
	"aggregate":     cmdAggregate,
	"markdown":      cmdMarkdown,
	"stats":         cmdStats,
	"stats-pages":   cmdStatsPages,
	"report":        cmdReport,
	"links":         cmdLinks,
	"wordfreq":      cmdWordfreq,
	"reconcile":     cmdReconcile,
	"domains":       cmdDomains,
	"headers":       cmdHeaders,
}

// exitCode lets a command fail without printing an error line (its own
// output already says why), like a Python script's sys.exit(1).
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "e2etool: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err := cmd(os.Args[2:]); err != nil {
		if code, ok := err.(exitCode); ok {
			os.Exit(int(code))
		}
		fmt.Fprintln(os.Stderr, "e2etool "+os.Args[1]+": "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	var names []string
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "usage: e2etool <"+strings.Join(names, "|")+"> [args]")
}

// env returns a required environment variable.
func env(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("$%s is not set", name)
	}
	return v, nil
}

// writeAtomic writes data to path through a temp file and rename.
func writeAtomic(path, tmp string, data []byte) error {
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
