package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/schedule"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
)

// swim schedule is swim queue add with a start time (features/schedule.md
// shipped): the batch is pinned and approved now, and no worker claims it
// before its start time; from then on it waits for its lanes as any batch
// does. Batches queued after it that depend on its lanes are held until it
// has run.

var scheduleNow = time.Now // tests move the clock

func init() {
	launcher.ScheduledFor = func(root string, n int) string {
		if e := scheduledHolding(root, n); e != nil {
			return scheduledAt(e) + " in " + e.ID
		}
		return ""
	}
}

func cmdSchedule(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "list", "ls":
			return scheduleList(args[1:])
		case "rm":
			return scheduleRm(args[1:])
		case "pause", "resume":
			return scheduleSetPaused(args[0] == "pause", args[1:])
		}
	}
	return scheduleAdd(args, "swim schedule")
}

var (
	ampmRE  = regexp.MustCompile(`^(?i)[ap]\.?m\.?$`)
	dateArg = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// splitTime takes the time off the end of args: the last argument, or the
// last two when they're "1:30 pm", "tomorrow 06:00" or "2026-10-20 13:30".
func splitTime(args []string) (lanes []string, when string, err error) {
	if len(args) < 2 {
		return nil, "", usagef("usage: swim schedule LANES... TIME (e.g. swim schedule 1 13:30)")
	}
	n := len(args)
	if n >= 3 && (ampmRE.MatchString(args[n-1]) || strings.EqualFold(args[n-2], "tomorrow") || dateArg.MatchString(args[n-2])) {
		return args[:n-2], args[n-2] + " " + args[n-1], nil
	}
	return args[:n-1], args[n-1], nil
}

type scheduleFlags struct {
	approve                 []string
	note, parallel, keyPath string
	tz, until, forD         string
	yes, asYAML, daily      bool
}

// parseScheduleFlags reads swim schedule's flags (--approve repeats).
func parseScheduleFlags(args []string) ([]string, scheduleFlags, error) {
	var f scheduleFlags
	var rest0 []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--approve" && i+1 < len(args):
			f.approve = append(f.approve, strings.Split(args[i+1], ",")...)
			i++
		case strings.HasPrefix(a, "--approve="):
			f.approve = append(f.approve, strings.Split(strings.TrimPrefix(a, "--approve="), ",")...)
		case a == "-y":
			f.yes = true
		default:
			rest0 = append(rest0, a)
		}
	}
	rest, err := flags{bools: map[string]*bool{"yes": &f.yes, "yaml": &f.asYAML, "daily": &f.daily},
		strs: map[string]*string{"note": &f.note, "parallel": &f.parallel, "key": &f.keyPath, "tz": &f.tz, "until": &f.until, "for": &f.forD}}.parse(rest0)
	return rest, f, err
}

func scheduleAdd(args []string, cmd string) error {
	rest, f, err := parseScheduleFlags(args)
	if err != nil {
		return err
	}
	if f.yes {
		return usagef("%s doesn't take -y: approving every guard of a run hours away, with no one watching, is what -y suits least; name each with --approve FLAG", cmd)
	}
	refs, when, err := splitTime(rest)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return usagef("usage: %s LANES... TIME: name the lanes (the last argument is always the time)", cmd)
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	loc, err := schedule.Zone(or(f.tz, cfg.ScheduleTZ))
	if err != nil {
		return usagef("%v", err)
	}
	if err := refreshStatus(root, cfg); err != nil {
		return err
	}
	var lanes []int
	for _, a := range refs {
		n, err := laneRef(root, cfg, a, false)
		if err != nil {
			return err
		}
		lanes = append(lanes, n)
	}
	if f.daily {
		return recurringAdd(root, cfg, lanes, when, loc, f)
	}
	if f.until != "" || f.forD != "" {
		return usagef("--until and --for go with --daily")
	}
	now := scheduleNow()
	at, _, err := schedule.ParseTime(when, now, loc)
	if err != nil {
		return usagef("%v", err)
	}
	o := queueOpts{approve: f.approve, note: f.note, keyPath: f.keyPath, notBefore: at.UTC().Format(time.RFC3339), zone: loc.String()}
	if f.parallel != "" {
		n, err := strconv.Atoi(f.parallel)
		if err != nil || n < 0 {
			return usagef("--parallel needs 0 (unlimited) or a positive number, got %q", f.parallel)
		}
		o.parallel = &n
	}
	b, err := addBatch(root, cfg, lanes, o)
	if err != nil {
		return err
	}
	e := b.entry
	refreshStatus(root, cfg) // status.yml says it's scheduled
	warns := scheduleWarnings(root, cfg, e)
	if b.warn != "" {
		warns = append([]string{b.warn}, warns...)
	}
	if f.asYAML {
		doc := struct {
			Schema    string   `yaml:"schema"`
			ID        string   `yaml:"id"`
			Seq       int      `yaml:"seq"`
			Lanes     []int    `yaml:"lanes"`
			NotBefore string   `yaml:"not_before"`
			Zone      string   `yaml:"zone"`
			Approve   []string `yaml:"approve,omitempty"`
			Warnings  []string `yaml:"warnings"`
		}{"swim.schedule/v1", e.ID, e.Seq, lanes, e.NotBefore, e.Zone, e.Approve, warns}
		if doc.Warnings == nil {
			doc.Warnings = []string{}
		}
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return err
		}
		return enc.Close()
	}
	fmt.Println(schedule.Reply(lanes, at, now, loc))
	fmt.Printf("  %s (#%d) · swim schedule rm %s cancels\n", e.ID, e.Seq, e.ID)
	for _, w := range warns {
		fmt.Println("  warning: " + w)
	}
	return nil
}

// scheduleWarnings are what might keep batch e from starting at its time,
// or make it skip: no worker, a batch ahead of it on its lanes, a lane it
// depends on that holds a round that hasn't passed.
func scheduleWarnings(root string, cfg *config.Config, e *queue.Entry) []string {
	var out []string
	if !workerRunning(root) {
		at, _ := time.Parse(time.RFC3339, e.NotBefore)
		loc, _ := schedule.Zone(e.Zone)
		out = append(out, fmt.Sprintf("no swim watch is running here; nothing will start this at %s (swim watch, or swim watch --headless --detach)", schedule.Format(at, loc)))
	}
	mine := map[int]bool{}
	for _, l := range e.Lanes {
		mine[l.Lane] = true
	}
	entries, _ := queue.List(root, false)
	for _, o := range entries {
		if o.ID == e.ID || o.State == queue.Orphaned || o.StartKey() > e.StartKey() {
			continue
		}
		for _, l := range o.Lanes {
			if mine[l.Lane] {
				out = append(out, fmt.Sprintf("batch #%d (%s) holds swim %d and goes first", o.Seq, o.ID, l.Lane))
				break
			}
		}
	}
	edges := launcher.DepEdges(root, cfg)
	st, _ := status.Load(root)
	warned := map[int]bool{}
	for _, l := range e.Lanes {
		for _, d := range edges[l.Lane] {
			if mine[d] || warned[d] {
				continue
			}
			info, _ := lane.ReadScript(root, d)
			var last *status.Lane
			if st != nil {
				last = st.Get(d)
			}
			if info.Pending() && !launcher.AlreadyPassed(info, last) {
				warned[d] = true
				out = append(out, fmt.Sprintf("swim %d depends on swim %d, which holds a round that hasn't passed: unless it passes by then, swim %d is skipped", l.Lane, d, l.Lane))
			}
		}
	}
	return out
}

// watchLock is the file every swim watch holds a shared lock on while it
// runs, so others can tell a worker is up.
func watchLock(root string) string { return filepath.Join(root, ".swim", "watch.lock") }

// holdWatchLock marks this swim watch as running; release undoes it.
func holdWatchLock(root string) (release func()) {
	os.MkdirAll(filepath.Dir(watchLock(root)), 0o755)
	f, err := os.OpenFile(watchLock(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH) != nil {
		f.Close()
		return func() {}
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }
}

// workerRunning reports whether a swim watch runs in this repo.
func workerRunning(root string) bool {
	f, err := os.OpenFile(watchLock(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// heldBy maps each waiting batch held by a schedule to the scheduled batch
// holding it: an earlier-queued scheduled batch that hasn't run yet holds
// a lane one of its lanes depends on, at any depth.
func heldBy(root string, cfg *config.Config, entries []*queue.Entry) map[string]*queue.Entry {
	var sched []*queue.Entry
	for _, e := range entries {
		if e.NotBefore != "" && (e.State == queue.Queued || e.State == queue.Running) {
			sched = append(sched, e)
		}
	}
	held := map[string]*queue.Entry{}
	if len(sched) == 0 {
		return held
	}
	edges := launcher.DepEdges(root, cfg)
	for _, b := range entries {
		if b.State != queue.Queued {
			continue
		}
		mine := map[int]bool{}
		for _, l := range b.Lanes {
			mine[l.Lane] = true
		}
	next:
		for _, l := range b.Lanes {
			up := launcher.Upstream(edges, l.Lane)
			for _, s := range sched {
				if s.ID == b.ID || s.Seq > b.Seq {
					continue
				}
				for _, sl := range s.Lanes {
					if up[sl.Lane] && !mine[sl.Lane] {
						held[b.ID] = s
						break next
					}
				}
			}
		}
	}
	return held
}

// scheduleTampered says why a signed scheduled batch must not run: its
// file no longer matches what was signed (its start time moved, say).
func scheduleTampered(e *queue.Entry) string {
	if e.NotBefore == "" || e.QueueSig == "" {
		return ""
	}
	if err := sign.Verify(e.QueuedKey, []byte(e.Statement()), e.QueueSig); err != nil {
		return fmt.Sprintf("%s's signature doesn't match its file (edited after it was scheduled, its start time moved?): refused", e.ID)
	}
	return ""
}

// scheduledHolding is the waiting scheduled batch that holds lane n, if any.
func scheduledHolding(root string, n int) *queue.Entry {
	entries, _ := queue.List(root, false)
	for _, e := range entries {
		if e.NotBefore == "" || e.State != queue.Queued {
			continue
		}
		for _, l := range e.Lanes {
			if l.Lane == n {
				return e
			}
		}
	}
	return nil
}

// scheduledAt says a scheduled batch's start: "13:30:00(CDT) 18:30:00(UTC)".
func scheduledAt(e *queue.Entry) string {
	at, _ := time.Parse(time.RFC3339, e.NotBefore)
	loc, err := schedule.Zone(e.Zone)
	if err != nil {
		loc = time.UTC
	}
	return schedule.Format(at, loc)
}

func scheduleList(args []string) error {
	var all, asYAML bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim schedule list [--all] [--yaml]")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	entries, err := queue.List(root, all)
	if err != nil {
		return err
	}
	var sched []*queue.Entry
	for _, e := range entries {
		if e.NotBefore != "" {
			sched = append(sched, e)
		}
	}
	queue.SortWaiting(sched)
	held := heldBy(root, cfg, entries)
	recs, _ := loadRecurring(root)
	if asYAML {
		type row struct {
			ID        string `yaml:"id"`
			Seq       int    `yaml:"seq"`
			State     string `yaml:"state"`
			Lanes     []int  `yaml:"lanes"`
			NotBefore string `yaml:"not_before"`
			Zone      string `yaml:"zone"`
			From      string `yaml:"from,omitempty"`
		}
		doc := struct {
			Schema    string       `yaml:"schema"`
			Batches   []row        `yaml:"batches"`
			Recurring []*recurring `yaml:"recurring"`
		}{Schema: "swim.schedule-list/v1", Batches: []row{}, Recurring: recs}
		for _, e := range sched {
			doc.Batches = append(doc.Batches, row{e.ID, e.Seq, e.State, laneNums(e), e.NotBefore, e.Zone, e.From})
		}
		if doc.Recurring == nil {
			doc.Recurring = []*recurring{}
		}
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return err
		}
		return enc.Close()
	}
	if len(sched) == 0 && len(recs) == 0 {
		fmt.Println("nothing scheduled (swim schedule LANES... TIME)")
		return nil
	}
	now := scheduleNow()
	for _, e := range sched {
		at, _ := time.Parse(time.RFC3339, e.NotBefore)
		when := schedule.Until(at, now)
		if e.State == queue.Done && e.Result != nil {
			when = e.Result.Result
		} else if e.State != queue.Queued {
			when = e.State
		}
		fmt.Printf("#%-4d %-24s %s  %-8s  %s  %s\n", e.Seq, e.ID, lanesText(laneNums(e)), e.State, scheduledAt(e), when)
	}
	for id, s := range held {
		if e, err := queue.Find(root, id); err == nil {
			fmt.Printf("#%-4d %-24s %s  held     until #%d (%s) has run\n", e.Seq, e.ID, lanesText(laneNums(e)), s.Seq, s.ID)
		}
	}
	for _, r := range recs {
		fmt.Println(r.line(now))
	}
	return nil
}

func scheduleRm(args []string) error {
	if len(args) != 1 {
		return usagef("usage: swim schedule rm ID")
	}
	root, cfg, err := repo()
	if err != nil {
		return err
	}
	if strings.HasPrefix(args[0], recurringPrefix) {
		return recurringRm(root, args[0])
	}
	e, err := queue.Find(root, args[0])
	if err != nil {
		return err
	}
	if e.NotBefore == "" {
		return fmt.Errorf("%s (#%d) isn't scheduled: swim queue rm %s", e.ID, e.Seq, e.ID)
	}
	entries, _ := queue.List(root, false)
	var released []string
	for id, s := range heldBy(root, cfg, entries) {
		if s.ID == e.ID {
			if b, err := queue.Find(root, id); err == nil {
				released = append(released, fmt.Sprintf("#%d (%s)", b.Seq, b.ID))
			}
		}
	}
	if err := queue.Remove(e); err != nil {
		return err
	}
	status.UpdateFile(root, cfg.Lanes, func(f *status.File) error {
		for _, l := range e.Lanes {
			if sl := f.Get(l.Lane); sl != nil {
				sl.ScheduleRemoved = e.ID // a dependent's skip says why
			}
		}
		return nil
	})
	refreshStatus(root, cfg)
	history.Log(root, history.Entry{Event: history.ScheduleRm, Detail: fmt.Sprintf("%s: %s, was scheduled for %s", e.ID, lanesText(laneNums(e)), e.NotBefore)})
	fmt.Printf("removed %s (#%d): %s, scheduled for %s\n", e.ID, e.Seq, lanesText(laneNums(e)), scheduledAt(e))
	if len(released) > 0 {
		sort.Strings(released)
		fmt.Printf("  released %s: their lanes depend on it, and its round never ran, so they'll be skipped unless it passes first (swim queue rm to remove them too)\n", strings.Join(released, ", "))
	}
	return nil
}

// claimDue claims the first waiting batch a worker may start now: due (a
// scheduled one not before its time) and not held by a schedule, in
// start-time order.
func claimDue(root string, cfg *config.Config) (*queue.Entry, error) {
	entries, err := queue.List(root, false)
	if err != nil {
		return nil, err
	}
	queue.SortWaiting(entries)
	held := heldBy(root, cfg, entries)
	now := scheduleNow()
	for _, w := range entries {
		if w.State != queue.Queued || !w.Due(now) || held[w.ID] != nil {
			continue
		}
		if e, err := queue.Claim(root, w, operator(), cfg.QueueLeaseFor()); err == nil {
			return e, nil
		}
	}
	return nil, nil
}

// nextScheduled says when the next scheduled batch starts, for a worker's
// activity: "; next #3 (swim 1) at 13:30:00(CDT) 18:30:00(UTC), in 2h10m".
func nextScheduled(root string) string {
	entries, _ := queue.List(root, false)
	queue.SortWaiting(entries)
	now := scheduleNow()
	for _, e := range entries {
		if e.State == queue.Queued && e.NotBefore != "" && !e.Due(now) {
			at, _ := time.Parse(time.RFC3339, e.NotBefore)
			return fmt.Sprintf("; next #%d (%s) at %s, %s", e.Seq, lanesText(laneNums(e)), scheduledAt(e), schedule.Until(at, now))
		}
	}
	return ""
}

// scheduledSummary is each waiting scheduled batch, for swim doctor.
func scheduledSummary(root string) []string {
	entries, _ := queue.List(root, false)
	queue.SortWaiting(entries)
	var out []string
	for _, e := range entries {
		if e.State == queue.Queued && e.NotBefore != "" {
			out = append(out, fmt.Sprintf("#%d %s at %s", e.Seq, lanesText(laneNums(e)), scheduledAt(e)))
		}
	}
	return out
}
