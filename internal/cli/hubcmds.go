package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// A headless worker's commands from its hub: it long-polls the hub with
// its own token (the hub never connects to it), and for each command
// checks that it's new (the hub's epoch and seq, and its id, kept in
// .swim/hub.yml), that it hasn't expired (capped by swim.yml's
// headless.command_ttl, and 2m for interrupt), that swim.yml's
// headless.allow takes it, and that what it's pinned to still holds. Then
// it carries it out, records it in .swim.log and the chain (refusals
// too), and acknowledges it. The hub decides nothing; this does.

// recentCIDs is how many command ids hub.yml remembers.
const recentCIDs = 200

// headlessCommands runs the command loop until stop is called. drain asks
// the worker to stop after the batch in hand (what SIGTERM does).
func headlessCommands(root string, every time.Duration, drain func()) (stop func()) {
	h, err := loadHub(root)
	if err != nil {
		return func() {}
	}
	c := hubClient(h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait := hub.MaxWait
		for ctx.Err() == nil {
			// The reporter may have enrolled again (a new token).
			if cur, err := loadHub(root); err == nil {
				h, c.WorkerToken, c.ID = cur, cur.WorkerToken, cur.ID
			}
			epoch, cmds, err := c.Poll(ctx, h.LastSeq, h.Epoch, wait)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && wait > 0 {
					// Something between here and the hub cuts long requests.
					wait = 0
					fmt.Fprintln(os.Stderr, "swim watch: the hub's long poll timed out; asking for commands every "+every.String()+" instead")
				}
				sleepCtx(ctx, every)
				continue
			}
			for _, cmd := range takeCommands(root, epoch, cmds) {
				outcome, detail := carryOut(root, h.URL, c, cmd, drain)
				if err := c.Ack(cmd.CID, outcome, detail); err != nil {
					fmt.Fprintln(os.Stderr, "swim watch: hub: ack "+cmd.Kind+": "+err.Error())
				}
			}
			if wait == 0 {
				sleepCtx(ctx, every)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// takeCommands keeps the commands not carried out before and records them
// as taken in .swim/hub.yml, before any is carried out: a worker that dies
// mid-command never repeats it.
func takeCommands(root, epoch string, cmds []hub.Command) []hub.Command {
	hubFileMu.Lock()
	defer hubFileMu.Unlock()
	h, err := loadHub(root)
	if err != nil {
		return nil
	}
	fresh, ignored, changed := acceptCommands(h, epoch, cmds)
	for _, c := range ignored {
		fmt.Fprintf(os.Stderr, "swim watch: hub: ignored %s (seq %d, %s): carried out already\n", ui.Safe(c.Kind), c.Seq, ui.Safe(c.CID))
	}
	if changed {
		if err := saveHub(root, h); err != nil {
			fmt.Fprintln(os.Stderr, "swim watch: hub: can't record the commands taken ("+err.Error()+"); not carrying them out")
			return nil
		}
	}
	return fresh
}

// acceptCommands is the replay check: in the hub's epoch (a new one starts
// the seq over), a command must come after the last seq taken, and its id
// must not be one taken recently. h records what it accepts.
func acceptCommands(h *hubFile, epoch string, cmds []hub.Command) (fresh, ignored []hub.Command, changed bool) {
	if epoch != h.Epoch {
		h.Epoch, h.LastSeq, changed = epoch, 0, true
	}
	sort.Slice(cmds, func(a, b int) bool { return cmds[a].Seq < cmds[b].Seq })
	for _, c := range cmds {
		if c.Seq <= h.LastSeq || c.CID == "" || slices.Contains(h.RecentCIDs, c.CID) {
			ignored = append(ignored, c)
			continue
		}
		h.LastSeq = c.Seq
		h.RecentCIDs = append(h.RecentCIDs, c.CID)
		if len(h.RecentCIDs) > recentCIDs {
			h.RecentCIDs = h.RecentCIDs[len(h.RecentCIDs)-recentCIDs:]
		}
		fresh, changed = append(fresh, c), true
	}
	return fresh, ignored, changed
}

// hubUser is who sent a command, as this worker records it: the hub
// wrote it, so it's made safe and kept short.
func hubUser(by string) string {
	by = strings.ReplaceAll(ui.Safe(by), " ", "_")
	if by == "" {
		by = "(nobody)"
	}
	if len(by) > 64 {
		by = by[:64]
	}
	return by
}

// commandDeadline is when cmd stops counting: its expiry, capped by
// headless.command_ttl (2m for interrupt) after it was sent. A send time
// in the future counts as now.
func commandDeadline(cfg *config.Config, cmd hub.Command, now time.Time) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, cmd.At)
	if err != nil {
		return time.Time{}, fmt.Errorf("no send time")
	}
	if at.After(now) {
		at = now
	}
	end := at.Add(cfg.CommandTTL(cmd.Kind))
	if exp, err := time.Parse(time.RFC3339, cmd.Expires); err == nil && exp.Before(end) {
		end = exp
	}
	return end, nil
}

// carryOut checks one command, does it, and records it: its outcome
// (done, refused or expired) and why.
func carryOut(root, url string, c *hub.Client, cmd hub.Command, drain func()) (outcome, detail string) {
	by := hubUser(cmd.By)
	kind := ui.Safe(cmd.Kind)
	cfg, err := loadCfg(root)
	switch {
	case err != nil:
		outcome, detail = hub.OutcomeRefused, "config: "+err.Error()
	default:
		outcome, detail = checkAndDo(root, cfg, c, cmd, by, url, drain)
	}
	text := outcome
	if detail != "" {
		text += ": " + detail
	}
	run := ui.Safe(cmd.Pin.Run)
	line := fmt.Sprintf("hub %s by %s via %s: %s", kind, by, url, text)
	history.Log(root, history.Entry{Event: history.Hub, Lane: cmd.Lane, Run: run, Detail: line})
	if cfg != nil {
		record(root, cfg, chain.Entry{Type: "hub", Method: kind, Lane: cmd.Lane, Run: run, Script: ui.Safe(cmd.Pin.SHA256),
			Manifest: ui.Safe(cmd.Pin.Manifest), Action: line, Result: outcome})
	}
	fmt.Println("swim watch: " + line)
	return outcome, detail
}

func checkAndDo(root string, cfg *config.Config, c *hub.Client, cmd hub.Command, by, url string, drain func()) (string, string) {
	now := time.Now()
	end, err := commandDeadline(cfg, cmd, now)
	if err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	if now.After(end) {
		return hub.OutcomeExpired, fmt.Sprintf("it expired at %s (headless.command_ttl %s)", end.UTC().Format(time.RFC3339), cfg.CommandTTL(cmd.Kind))
	}
	if access := hubAccessOf(root); !cfg.HubAllows(access, cmd.Kind) {
		why := fmt.Sprintf("swim.yml's headless.allow is [%s]", strings.Join(cfg.HeadlessAllow(), ", "))
		switch {
		case access == config.AccessRead:
			why = "this swimmer's access is read (swim auth login URL --access readwrite lets the hub send commands)"
		case access == config.AccessReadWrite:
			why = fmt.Sprintf("swim.yml's headless.allow narrows readwrite to [%s]", strings.Join(cfg.HubAllow(access), ", "))
		}
		return hub.OutcomeRefused, "not allowed here: " + why
	}
	switch cmd.Kind {
	case config.HubDrain, config.HubDequeue, config.HubRequeue:
	case config.HubQueue:
		for _, l := range cmd.Lanes {
			if !cfg.ValidLane(l.Lane) {
				return hub.OutcomeRefused, fmt.Sprintf("no lane %d", l.Lane)
			}
		}
	default:
		if !cfg.ValidLane(cmd.Lane) {
			return hub.OutcomeRefused, fmt.Sprintf("no lane %d", cmd.Lane)
		}
	}
	switch cmd.Kind {
	case config.HubInterrupt:
		if cmd.Pin.Run == "" {
			return hub.OutcomeRefused, "not pinned to a run"
		}
		msg, err := interruptLane(root, cmd.Lane, cmd.Pin.Run)
		if err != nil {
			return hub.OutcomeRefused, err.Error()
		}
		return hub.OutcomeDone, msg
	case config.HubDrain:
		drain()
		return hub.OutcomeDone, "the swimmer stops after the batch in hand"
	case config.HubRun:
		return hubRun(root, cfg, cmd, by, url)
	case config.HubLog:
		return hubLog(root, cfg, c, cmd)
	case config.HubTrash:
		return hubTrash(root, cfg, cmd, by, url)
	case config.HubPause, config.HubUnpause:
		return hubPause(root, cmd, by, url)
	case config.HubAnswer:
		return answerHub(cmd)
	case config.HubQueue:
		return hubQueue(root, cfg, cmd, by, url)
	case config.HubDequeue:
		return hubDequeue(root, cmd, by, url)
	case config.HubRequeue:
		return hubRequeue(root, cfg, cmd, by, url)
	}
	return hub.OutcomeRefused, "unknown command"
}

// runPin is what a run command from the hub pins lane n to: its script's
// hash and its manifest's ("" when it holds no pending round).
func runPin(root string, cfg *config.Config, n int) (string, string) {
	if info, err := lane.ReadScript(root, n); err != nil || !info.Pending() {
		return "", ""
	}
	data, err := lane.ScriptBytes(root, n)
	if err != nil {
		return "", ""
	}
	m, err := manifest.Build(root, cfg, n, "")
	if err != nil {
		return "", ""
	}
	return chain.Hash(data), m.SHA256
}

// hubRun queues lane n as the hub's user asked: an unsigned entry, by
// "hub:<user> via <url>", with no approvals of its own, and only while the
// script and its manifest are the ones the page showed.
func hubRun(root string, cfg *config.Config, cmd hub.Command, by, url string) (string, string) {
	n := cmd.Lane
	if cmd.Pin.SHA256 == "" || cmd.Pin.Manifest == "" {
		return hub.OutcomeRefused, "not pinned to a script and manifest"
	}
	l, _, err := pinLane(root, cfg, n)
	if err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	switch {
	case l.SHA256 != cmd.Pin.SHA256:
		return hub.OutcomeRefused, fmt.Sprintf("stale: swim %d's script is now %s, not the %s the page showed", n, short12(l.SHA256), short12(cmd.Pin.SHA256))
	case !l.PinsManifest(cmd.Pin.Manifest):
		return hub.OutcomeRefused, fmt.Sprintf("stale: swim %d's manifest is now %s, not the %s the page showed (what it sources, its runner or policy changed)", n, short12(l.Manifest), short12(cmd.Pin.Manifest))
	}
	e := &queue.Entry{By: "hub:" + by + " via " + url, Note: "run from the hub", Lanes: []queue.Lane{l}}
	if err := queue.Add(root, e); err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	history.Log(root, history.Entry{Event: history.QueueAdd, Detail: fmt.Sprintf("%s: swim %d  by %s (unsigned, no approvals)", e.ID, n, e.By)})
	return hub.OutcomeDone, fmt.Sprintf("queued %s (#%d): unsigned, with no approvals of its own", e.ID, e.Seq)
}

// hubQueue queues the command's lanes as one batch, as swim queue add
// would but as the hub's user asked: unsigned, by "hub:<user> via <url>",
// with no approvals of its own, and only while each lane's script and
// manifest are the ones the page showed.
func hubQueue(root string, cfg *config.Config, cmd hub.Command, by, url string) (string, string) {
	if len(cmd.Lanes) == 0 {
		return hub.OutcomeRefused, "no lanes to queue"
	}
	var lanes []queue.Lane
	var ns []string
	for _, p := range cmd.Lanes {
		l, err := pinnedAsShown(root, cfg, p.Lane, p.SHA256, p.Manifest)
		if err != nil {
			return hub.OutcomeRefused, err.Error()
		}
		lanes = append(lanes, l)
		ns = append(ns, strconv.Itoa(p.Lane))
	}
	e := &queue.Entry{By: "hub:" + by + " via " + url, Note: "queued from the hub", Lanes: lanes}
	if err := queue.Add(root, e); err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	history.Log(root, history.Entry{Event: history.QueueAdd, Detail: fmt.Sprintf("%s: swim %s  by %s (unsigned, no approvals)", e.ID, strings.Join(ns, ","), e.By)})
	return hub.OutcomeDone, fmt.Sprintf("queued %s (#%d): swim %s, unsigned, with no approvals of its own", e.ID, e.Seq, strings.Join(ns, ","))
}

// pinnedAsShown is lane n pinned as a queue entry holds it, refused when
// its script or manifest isn't the one the page showed.
func pinnedAsShown(root string, cfg *config.Config, n int, sha, mf string) (queue.Lane, error) {
	l, _, err := pinLane(root, cfg, n)
	if err != nil {
		return l, err
	}
	switch {
	case l.SHA256 != sha:
		return l, fmt.Errorf("stale: swim %d's script is now %s, not the %s the page showed", n, short12(l.SHA256), short12(sha))
	case !l.PinsManifest(mf):
		return l, fmt.Errorf("stale: swim %d's manifest is now %s, not the %s the page showed (what it sources, its runner or policy changed)", n, short12(l.Manifest), short12(mf))
	}
	return l, nil
}

// hubDequeue takes the batch the page showed off the queue, as swim queue
// rm would: not while it runs.
func hubDequeue(root string, cmd hub.Command, by, url string) (string, string) {
	e, err := queue.Find(root, cmd.Pin.Batch)
	if err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	if e.ID != cmd.Pin.Batch {
		return hub.OutcomeRefused, fmt.Sprintf("no queue batch %s", cmd.Pin.Batch)
	}
	if err := queue.Remove(e); err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	who := "hub:" + by + " via " + url
	history.Log(root, history.Entry{Event: history.QueueRm, Detail: fmt.Sprintf("%s (%s)  by %s", e.ID, e.State, who)})
	return hub.OutcomeDone, fmt.Sprintf("removed %s (#%d, %s) from the queue", e.ID, e.Seq, e.State)
}

// hubRequeue queues a finished (or orphaned) batch's lanes again as a new
// batch, by the hub's user: unsigned and with no approvals, unlike swim
// queue requeue, which keeps the batch's own approvals and signature (the
// hub mustn't bring those back). Each lane must still hold the script the
// batch ran; one that changed since is queued fresh instead.
func hubRequeue(root string, cfg *config.Config, cmd hub.Command, by, url string) (string, string) {
	old, err := queue.Find(root, cmd.Pin.Batch)
	if err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	if old.ID != cmd.Pin.Batch {
		return hub.OutcomeRefused, fmt.Sprintf("no queue batch %s", cmd.Pin.Batch)
	}
	if old.State != queue.Done && old.State != queue.Orphaned {
		return hub.OutcomeRefused, fmt.Sprintf("%s is %s: only a finished batch is queued again", old.ID, old.State)
	}
	var lanes []queue.Lane
	var ns []string
	for _, ol := range old.Lanes {
		l, _, err := pinLane(root, cfg, ol.Lane)
		if err != nil {
			return hub.OutcomeRefused, err.Error()
		}
		if l.SHA256 != ol.SHA256 {
			return hub.OutcomeRefused, fmt.Sprintf("swim %d's script changed since %s ran it (%s, then %s): queue it fresh instead", ol.Lane, old.ID, short12(ol.SHA256), short12(l.SHA256))
		}
		lanes = append(lanes, l)
		ns = append(ns, strconv.Itoa(l.Lane))
	}
	e := &queue.Entry{By: "hub:" + by + " via " + url, Note: "queued again from the hub (was " + old.ID + ")", Lanes: lanes}
	if err := queue.Add(root, e); err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	history.Log(root, history.Entry{Event: history.QueueAdd, Detail: fmt.Sprintf("%s: swim %s  by %s (again, was %s; unsigned, no approvals)", e.ID, strings.Join(ns, ","), e.By, old.ID)})
	return hub.OutcomeDone, fmt.Sprintf("queued %s (#%d) again as %s (#%d): unsigned, with no approvals of its own", old.ID, old.Seq, e.ID, e.Seq)
}

// hubPause pauses lane n, or lets it go on, as the hub's user asked, only
// while it's still in the run the page showed.
func hubPause(root string, cmd hub.Command, by, url string) (string, string) {
	n := cmd.Lane
	st, _ := status.Load(root)
	run := ""
	if st != nil {
		if l := st.Get(n); l != nil {
			run = l.Run
		}
	}
	if run != cmd.Pin.Run {
		return hub.OutcomeRefused, fmt.Sprintf("stale: swim %d is now run %s, not %s", n, or(run, "(none)"), cmd.Pin.Run)
	}
	who := "hub:" + by + " via " + url
	if cmd.Kind == config.HubUnpause {
		msg, was := unpauseOne(root, st, n, who)
		if !was {
			return hub.OutcomeRefused, msg
		}
		return hub.OutcomeDone, msg
	}
	msg, err := pauseOne(root, st, n, who, "from the hub")
	if err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	return hub.OutcomeDone, msg
}

// hubTrash trashes lane n as the hub's user asked, only while it holds
// the job the page showed; every swim trash refusal applies, and an
// Always lane is never forced from here.
func hubTrash(root string, cfg *config.Config, cmd hub.Command, by, url string) (string, string) {
	if cmd.Pin.Job == "" {
		return hub.OutcomeRefused, "not pinned to a job"
	}
	t, err := trashLane(root, cfg, trashReq{n: cmd.Lane, job: cmd.Pin.Job, by: "hub:" + by + " via " + url, from: trashFromHub})
	if err != nil {
		return hub.OutcomeRefused, err.Error()
	}
	return hub.OutcomeDone, fmt.Sprintf("trashed swim %d (.swim/cleaned/%s/); swim trash --restore %s puts it back", cmd.Lane, t.stamp, t.stamp)
}

// hubLog uploads the log of lane n's rounds in the command's run. The log
// is masked already as it's written; it's masked again with the secrets
// known now, as a backstop. Past hub.LogMax, only its end goes.
func hubLog(root string, cfg *config.Config, c *hub.Client, cmd hub.Command) (string, string) {
	n, run := cmd.Lane, cmd.Pin.Run
	if !cfg.HeadlessLogs() {
		return hub.OutcomeRefused, "swim.yml sets headless.logs: off: logs stay on this machine"
	}
	if run == "" {
		return hub.OutcomeRefused, "not pinned to a run"
	}
	var lines []string
	for _, f := range append(archives(root, n), lane.Log(root, n)) {
		rounds, _ := roundsOf(f, run)
		for _, r := range rounds {
			lines = append(lines, r...)
		}
	}
	if len(lines) == 0 {
		return hub.OutcomeRefused, fmt.Sprintf("no log of swim %d for run %s here", n, run)
	}
	text := cfg.Masker(os.Environ()).String(strings.Join(lines, "\n") + "\n")
	if len(text) > hub.LogMax {
		cut := len(text) - (hub.LogMax - 200)
		if i := strings.IndexByte(text[cut:], '\n'); i >= 0 {
			cut += i + 1
		}
		text = fmt.Sprintf("… (the first %d bytes are cut: the hub takes 8 MiB at most)\n", cut) + text[cut:]
	}
	if err := c.UploadLog(run, n, []byte(text)); err != nil {
		return hub.OutcomeRefused, "upload: " + err.Error()
	}
	return hub.OutcomeDone, fmt.Sprintf("uploaded %d bytes", len(text))
}
