package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/gates-brightly/swimlane/internal/checkpoint"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdCheckpoint lists and forgets lanes' checkpoints (run --checkpoint):
//
//	swim checkpoint [ls] [N|JOB]       what the next run of each lane keeps
//	swim checkpoint rm N|JOB KEY...    run those steps again next time
//	swim checkpoint rm N|JOB --all     run every one again (as swim run --fresh)
func cmdCheckpoint(args []string) error {
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	sub := "ls"
	if len(args) > 0 && (args[0] == "ls" || args[0] == "rm") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "rm":
		return checkpointRm(root, cfg, args)
	}
	if len(args) > 1 {
		return usagef("usage: swim checkpoint [ls] [N|JOB]")
	}
	var lanes []int
	if len(args) == 1 {
		n, err := laneRef(root, cfg, args[0], true)
		if err != nil {
			return err
		}
		lanes = []int{n}
	} else {
		lanes = lane.Associated(root)
	}
	p := painter()
	any := false
	for _, n := range lanes {
		f, err := checkpoint.Load(root, n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "swim checkpoint: "+err.Error())
			continue
		}
		if len(f.Steps) == 0 {
			if len(args) == 1 {
				fmt.Printf("swim %d: no checkpoints\n", n)
			}
			continue
		}
		any = true
		info, _ := lane.ReadScript(root, n)
		head := fmt.Sprintf("swim %d: %d checkpoints", n, len(f.Steps))
		if f.Round != checkpoint.Round(info.Job, info.Round) {
			head += p.Paint(ui.Dim, " (of another round: the next run keeps none)")
		} else {
			head += " (the next run keeps them; swim run --fresh runs them all)"
		}
		fmt.Println(head)
		for _, k := range f.Keys() {
			s := f.Steps[k]
			when := s.At
			if t, err := time.Parse(time.RFC3339, s.At); err == nil {
				when = t.Local().Format("2006-01-02 15:04:05")
			}
			line := "  " + k
			if s.Label != "" && s.Label != k {
				line += "  " + s.Label
			}
			fmt.Println(line + p.Paint(ui.Dim, "  passed "+when+" in "+s.Run))
		}
	}
	if !any && len(args) == 0 {
		fmt.Println("no checkpoints (a step marked run --checkpoint records one when it passes)")
	}
	return nil
}

func checkpointRm(root string, cfg *config.Config, args []string) error {
	all := false
	var rest []string
	for _, a := range args {
		if a == "--all" {
			all = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 || all == (len(rest) > 1) {
		return usagef("usage: swim checkpoint rm N|JOB KEY... | swim checkpoint rm N|JOB --all")
	}
	n, err := laneRef(root, cfg, rest[0], true)
	if err != nil {
		return err
	}
	if all {
		if err := checkpoint.Clear(root, n); err != nil {
			return err
		}
		history.Log(root, history.Entry{Event: history.Note, Lane: n, Detail: "checkpoints cleared (swim checkpoint rm --all)"})
		fmt.Printf("swim %d: checkpoints cleared; the next run runs every step\n", n)
		return nil
	}
	gone, err := checkpoint.Forget(root, n, rest[1:])
	if err != nil {
		return err
	}
	for _, k := range rest[1:] {
		found := false
		for _, g := range gone {
			found = found || g == k
		}
		if !found {
			fmt.Fprintf(os.Stderr, "swim checkpoint: swim %d has no checkpoint %s (swim checkpoint %d lists them)\n", n, k, n)
		}
	}
	if len(gone) == 0 {
		return exitError{1}
	}
	history.Log(root, history.Entry{Event: history.Note, Lane: n, Detail: "checkpoints forgotten: " + joinStrs(gone)})
	fmt.Printf("swim %d: forgot %s; the next run runs %s again\n", n, plural(len(gone), "checkpoint"), map[bool]string{true: "it", false: "them"}[len(gone) == 1])
	return nil
}

func joinStrs(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += strconv.Quote(s)
	}
	return out
}
