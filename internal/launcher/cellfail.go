package launcher

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// A CellFailure is a matrix cell that didn't pass, and why, as its log
// records it.
type CellFailure struct {
	Cell  string // the cell's name
	Ref   string // how swim log names it: 3[orders]
	State string // how it ended: failed, interrupted, skipped
	Step  string // the first failed step (or STOP): "FAIL  test (exit 1)"; "" if none
	Line  string // the first line that step's command printed; "" if none
}

// CellFailures lists the cells of matrix lane n that ended without
// passing (states: each cell's end state), in the matrix's order, with the first
// failed step and its first output line from the cell's log. A cell still
// running or not run isn't listed.
func CellFailures(root string, n int, info lane.Info, states map[string]string) []CellFailure {
	var out []CellFailure
	for c, s := range states {
		switch s {
		case status.Failed, status.Interrupted, status.Skipped:
		default:
			continue
		}
		f := CellFailure{Cell: c, Ref: info.SubLabel(n, c), State: s}
		if r, err := logparse.ParseFile(lane.CellLog(root, n, c)); err == nil {
			if failed := r.Failed(); len(failed) > 0 {
				f.Step = failed[0].Text()
				for _, l := range failed[0].Output {
					if strings.TrimSpace(l) != "" {
						f.Line = strings.TrimSpace(l)
						break
					}
				}
			}
		}
		out = append(out, f)
	}
	// In the matrix's order, as swim log N lists the cells.
	at := map[string]int{}
	for i, c := range info.Cells() {
		at[c.Name] = i
	}
	sort.Slice(out, func(i, j int) bool {
		if at[out[i].Cell] != at[out[j].Cell] {
			return at[out[i].Cell] < at[out[j].Cell]
		}
		return out[i].Cell < out[j].Cell
	})
	return out
}

// PrintCellFailures writes each failed cell: its reference and how it
// ended, its first failed step, the line its command printed and how to
// read its log, each line after the first indented under it.
func PrintCellFailures(w io.Writer, p ui.Painter, indent string, fs []CellFailure) {
	for _, f := range fs {
		head := f.Ref + " " + strings.ToUpper(f.State)
		if f.Step != "" {
			head += "  " + f.Step
		}
		fmt.Fprintln(w, indent+p.Paint(ui.Red, head))
		if f.Line != "" {
			fmt.Fprintln(w, indent+"    | "+f.Line)
		}
		fmt.Fprintln(w, indent+"    "+p.Paint(ui.Dim, "swim log '"+f.Ref+"'"))
	}
}
