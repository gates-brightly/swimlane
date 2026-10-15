package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// swim trash moves one lane's files aside: its script (any language) and
// sidecar go to .swim/cleaned/<stamp>-lane<N>/, as swim clean sets scripts
// aside, and its current log is archived. Nothing is deleted: swim trash
// --restore (or swim clean --restore) puts it back, and swim prune deletes
// it for good later. swim watch's d and the hub's Trash button are front
// ends for trashLane.

// Where a lane was trashed from.
const (
	trashFromCLI   = "cli"
	trashFromWatch = "watch"
	trashFromHub   = "hub"
)

// trashReq is one lane to trash, and who asks.
type trashReq struct {
	n     int
	job   string // a pin: refused unless the lane still holds this job, or a script with this SHA-256 ("" for none)
	by    string // who trashed it (the operator, or hub:<user> via <url>)
	from  string // cli | watch | hub
	force bool   // an Always lane too (the CLI only)
	dry   bool   // say what would move; change nothing
}

// trashed is what trashLane did (or, dry, would do).
type trashed struct {
	stamp    string
	files    []string
	archived string // the archived log's name ("" if the lane had none)
}

// trashLane trashes one lane, or says why it won't. Every front end goes
// through it, so the refusals are the same everywhere.
func trashLane(root string, cfg *config.Config, r trashReq) (*trashed, error) {
	n := r.n
	if _, err := os.Stat(lane.Script(root, n)); errors.Is(err, os.ErrNotExist) {
		if _, group := lane.GroupOf(root, n); !group {
			return nil, fmt.Errorf("swim %d has no lane script to trash", n)
		}
	}
	info, err := lane.ReadScript(root, n)
	if err != nil {
		return nil, err
	}
	if _, ok := lane.GroupOf(root, n); ok {
		return nil, fmt.Errorf("swim %d is a group file's lane: its jobs are committed, standing work; edit %s instead", n, lane.ScriptName(root, n))
	}
	script := lane.ScriptName(root, n)
	if script == "" {
		return nil, fmt.Errorf("swim %d has no lane script to trash", n)
	}
	if r.job != "" && trashPin(root, n, info) != r.job && !(info.Job != "" && lane.MatchJob(info.Job, r.job)) {
		return nil, fmt.Errorf("swim %d holds %s now, not %s (rewritten since): nothing moved", n, short8(trashPin(root, n, info)), short8(r.job))
	}
	st, _ := status.Load(root)
	if why := laneBusy(root, st, n); why != "" {
		return nil, fmt.Errorf("%s: swim interrupt %d first", why, n)
	}
	if entries, err := queue.List(root, false); err == nil {
		for _, e := range entries {
			if e.State != queue.Queued && e.State != queue.Running {
				continue
			}
			for _, l := range e.Lanes {
				if l.Lane != n {
					continue
				}
				what, rm := "a queued batch", fmt.Sprintf("swim queue rm %d", e.Seq)
				if e.NotBefore != "" {
					what, rm = "a scheduled batch", fmt.Sprintf("swim schedule rm %d", e.Seq)
				}
				return nil, fmt.Errorf("swim %d is in %s, #%d, which is pinned to its script: %s first", n, what, e.Seq, rm)
			}
		}
	}
	if info.Always && !r.force {
		how := "swim trash " + strconv.Itoa(n) + " --force does it"
		if r.from != trashFromCLI {
			how = "only swim trash --force at a terminal does it"
		}
		return nil, fmt.Errorf("swim %d is an Always lane (# Always: yes): %s", n, how)
	}

	files := []string{script}
	if sc := lane.Sidecar(root, n); sc != "" {
		if _, err := os.Stat(sc); err == nil {
			files = append(files, rel(root, sc))
		}
	}
	stamp := trashStamp(root, n, time.Now())
	out := &trashed{stamp: stamp, files: files}
	if r.dry {
		return out, nil
	}
	moved, err := moveScripts(root, stamp, files, func(m *cleanedManifest) {
		m.Lane, m.Job, m.Round, m.From = n, info.Job, info.Round, r.from
		if r.by != "" {
			m.By = r.by
		}
	})
	if err != nil {
		return nil, err
	}
	if hasLog(root, n) {
		dst, err := archiveLane(root, cfg, n, "trash-"+stamp)
		if err != nil {
			return nil, err
		}
		out.archived = filepath.Base(dst)
	}
	refreshStatus(root, cfg)
	detail := fmt.Sprintf("%s: %s (from %s)", stamp, strings.Join(files, " "), r.from)
	if r.by != "" && r.from == trashFromHub {
		detail += " by " + r.by
	}
	history.Log(root, history.Entry{Event: history.Trash, Lane: n, Job: info.Job, Detail: detail})
	if err := record(root, cfg, chain.Entry{Type: "trash", Lane: n, Job: info.Job, Action: detail, Outputs: sums(moved)}); err != nil {
		return out, err
	}
	return out, nil
}

// trashPin is what a trash command from the hub pins lane n to: its job,
// or for a script with no # Job: line, the script's SHA-256.
func trashPin(root string, n int, info lane.Info) string {
	if info.Job != "" {
		return info.Job
	}
	data, err := lane.ScriptBytes(root, n)
	if err != nil {
		return ""
	}
	return chain.Hash(data)
}

// trashStamp names a trashed lane's entry: <YYYYMMDD-HHMM>-lane<N>, made
// unique (-2, -3, ...) if trashed twice in a minute.
func trashStamp(root string, n int, t time.Time) string {
	stamp := fmt.Sprintf("%s-lane%d", t.Format("20060102-1504"), n)
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(cleanedDir(root), stamp)); errors.Is(err, os.ErrNotExist) {
			return stamp
		}
		stamp = fmt.Sprintf("%s-lane%d-%d", t.Format("20060102-1504"), n, i)
	}
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// cmdTrash is swim trash: trash lanes, list the trashed ones, or restore one.
func cmdTrash(args []string) error {
	var force, dry bool
	var restore string
	for i, a := range args {
		if a == "-n" {
			args[i] = "--dry-run"
		}
	}
	rest, err := flags{bools: map[string]*bool{"force": &force, "dry-run": &dry},
		strs: map[string]*string{"restore": &restore}}.parse(args)
	if err != nil {
		return err
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if restore != "" {
		if len(rest) > 0 {
			return usagef("usage: swim trash --restore STAMP")
		}
		return cleanRestore(root, cfg, restore)
	}
	if len(rest) == 0 {
		return listTrash(root)
	}
	var ns []int
	for _, a := range rest {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		ns = append(ns, n)
	}
	if len(ns) > 1 && !dry && ui.IsTTY(os.Stdin) && ui.IsTTY(os.Stdout) {
		fmt.Printf("trash %d lanes (swim %s)? Their files move to .swim/cleaned/ and can be restored. [y/N] ", len(ns), joinNs(ns))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Println("nothing trashed")
			return nil
		}
	}
	var failed []string
	for _, n := range ns {
		t, err := trashLane(root, cfg, trashReq{n: n, by: operator(), from: trashFromCLI, force: force, dry: dry})
		if err != nil {
			failed = append(failed, err.Error())
			fmt.Fprintln(os.Stderr, "swim trash: "+err.Error())
			continue
		}
		if dry {
			fmt.Printf("would trash swim %d: move %s to .swim/cleaned/%s/\n", n, strings.Join(t.files, " "), t.stamp)
			continue
		}
		msg := fmt.Sprintf("trashed swim %d: %s → .swim/cleaned/%s/", n, strings.Join(t.files, " "), t.stamp)
		if t.archived != "" {
			msg += "; log archived as " + t.archived
		}
		fmt.Println(msg)
		fmt.Printf("  restore: swim trash --restore %s\n", t.stamp)
	}
	if dry {
		fmt.Println("dry run: nothing changed")
	}
	if len(failed) > 0 {
		return exitError{1}
	}
	return nil
}

// trashEntries are the lanes swim trash has set aside, from their
// manifests under .swim/cleaned/ (a clean's own entries have no lane).
func trashEntries(root string) []cleanedManifest {
	dirs, _ := os.ReadDir(cleanedDir(root))
	var ms []cleanedManifest
	for _, d := range dirs {
		data, err := os.ReadFile(filepath.Join(cleanedDir(root), d.Name(), "cleaned.yml"))
		if err != nil {
			continue
		}
		var m cleanedManifest
		if yaml.Unmarshal(data, &m) == nil && m.Lane > 0 {
			ms = append(ms, m)
		}
	}
	return ms
}

// trashedNote says where lane n's round went, for a lane that has no
// script because it was trashed: "swim 5 was trashed (...); swim trash
// --restore STAMP puts it back". "" when lane n has a script, or no trash
// entry of its own.
func trashedNote(root string, n int) string {
	if len(lane.Scripts(root, n)) > 0 {
		return ""
	}
	if _, ok := lane.LaneJobs(root, n); ok {
		return ""
	}
	var last *cleanedManifest
	for _, m := range trashEntries(root) {
		if m.Lane == n && (last == nil || m.At > last.At) {
			m := m
			last = &m
		}
	}
	if last == nil {
		return ""
	}
	return fmt.Sprintf("swim %d was trashed (%s, by %s from %s, %s); swim trash --restore %s puts it back", n, last.Stamp, last.By, last.From, last.At, last.Stamp)
}

func joinNs(ns []int) string {
	var s []string
	for _, n := range ns {
		s = append(s, strconv.Itoa(n))
	}
	return strings.Join(s, " ")
}

// listTrash lists the trashed lanes under .swim/cleaned/, newest first.
func listTrash(root string) error {
	ms := trashEntries(root)
	if len(ms) == 0 {
		fmt.Println("nothing trashed (swim trash N moves a lane's files aside)")
		return nil
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].At > ms[j].At })
	fmt.Printf("%-24s %-8s %-9s %-20s %-6s %s\n", "stamp", "lane", "job", "when", "from", "by · round")
	for _, m := range ms {
		fmt.Printf("%-24s swim %-3d %-9s %-20s %-6s %s · %s\n", m.Stamp, m.Lane, short8(m.Job), m.At, m.From, m.By, m.Round)
	}
	fmt.Println("restore one: swim trash --restore STAMP")
	return nil
}
