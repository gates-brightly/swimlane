package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Events a Note is about (SWIM_EVENT).
const (
	EventAsk  = "ask"  // a lane waits on the operator
	EventEnd  = "end"  // a run or a batch ended
	EventTest = "test" // swim config's t: is the hook working?
)

// Note is one notification, as a hook gets it: what the terminal shows
// (Title, Body) and what a script would branch on.
type Note struct {
	Event  string // EventAsk, EventEnd or EventTest
	Result string // for EventEnd: passed, failed, interrupted, skipped (a guard dry-ran) or error
	Title  string
	Body   string
	Lanes  []int
	Batch  int    // swim watch's batch number (0: a swim run)
	Repo   string // where it came from (Origin.Repo)
	Branch string // the branch checked out there ("": detached or outside git)
	// For EventAsk under swim watch with phone set: the question's links,
	// one tap each, and when they stop working.
	ApproveURL, RejectURL string
	Expires               time.Time
}

// Origin is where a run's or a watch's notifications come from: every
// title starts with it ("swimlane · lane 2 needs you"), and the branch
// when it isn't the default ("swimlane@fix-auth · run failed"), since one
// operator runs swims in many repos and worktrees, with the same lane
// numbers. Worked out once per run or watch.
type Origin struct {
	Repo    string
	Branch  string // "" when detached or outside git
	Default string // the repo's default branch, origin/HEAD's ("": unknown)
}

// Title is what, said from o: "<repo>[@<branch>] · what".
func (o Origin) Title(what string) string {
	if o.Repo == "" {
		return what
	}
	from := o.Repo
	switch o.Branch {
	case "", o.Default, "main", "master":
	default:
		from += "@" + o.Branch
	}
	return from + " · " + what
}

// Note is a Note from o, with its Repo and Branch.
func (o Origin) Note(n Note) Note {
	n.Repo, n.Branch = o.Repo, o.Branch
	return n
}

// NtfyCommand is what a Hook runs with notify_url and no notify_command:
// the note as an ntfy message (title, priority, tags, and a question's
// Approve and Decline buttons) POSTed to the URL. --fail-with-body makes
// an HTTP error fail, with what the server said.
const NtfyCommand = `curl -sS --fail-with-body -m 10 -H "Title: $SWIM_TITLE" -H "Priority: $SWIM_PRIORITY" -H "Tags: $SWIM_TAGS" -H "Actions: $SWIM_ACTIONS" -d "$SWIM_BODY" "$SWIM_NOTIFY_URL"`

// hookTimeout is how long one hook command may take before it's killed.
const hookTimeout = 15 * time.Second

// HookConfig is what a Hook runs, and where it records how that went.
type HookConfig struct {
	Command string       // notify_command ("": NtfyCommand when URL is set)
	URL     string       // notify_url, the command's SWIM_NOTIFY_URL
	Root    string       // the repo: where the command runs
	State   string       // where each send is recorded (StatePath; "": nowhere)
	Warn    func(string) // hears of a send that failed (nil: nobody)
}

// Hook runs the operator's notify_command (or NtfyCommand, for notify_url)
// for each Note: `sh -c COMMAND` in the repo root, with the Note in its
// environment (SWIM_EVENT, SWIM_RESULT, SWIM_TITLE, SWIM_BODY, SWIM_LANES,
// SWIM_BATCH, SWIM_REPO, SWIM_BRANCH, SWIM_ROOT, SWIM_PRIORITY, SWIM_TAGS,
// SWIM_NOTIFY_URL, and a question's SWIM_APPROVE_URL, SWIM_REJECT_URL,
// SWIM_EXPIRES and SWIM_ACTIONS), so it
// can send the note anywhere. Send runs it in the background; Wait waits
// for those. A nil Hook runs nothing.
type Hook struct {
	c       HookConfig
	setting string // the setting it comes from, for messages
	running sync.WaitGroup
}

// NewHook returns the Hook c describes (nil: neither a command nor a URL).
func NewHook(c HookConfig) *Hook {
	c.Command, c.URL = strings.TrimSpace(c.Command), strings.TrimSpace(c.URL)
	h := &Hook{c: c, setting: "notify_command"}
	switch {
	case c.Command != "":
	case c.URL != "":
		h.c.Command, h.setting = NtfyCommand, "notify_url"
	default:
		return nil
	}
	return h
}

// Send starts the command for n and returns at once.
func (h *Hook) Send(n Note) {
	if h == nil {
		return
	}
	h.running.Add(1)
	go func() {
		defer h.running.Done()
		if err := h.SendNow(n); err != nil && h.c.Warn != nil {
			h.c.Warn(err.Error())
		}
	}()
}

// SendNow runs the command for n and says how it went, recording that.
func (h *Hook) SendNow(n Note) error {
	if h == nil {
		return errors.New("no notify_url or notify_command set")
	}
	err := h.run(n)
	if err != nil {
		err = fmt.Errorf("%s: %w", h.setting, err)
	}
	h.record(n, err)
	return err
}

// Wait waits for the commands Send started (each ends by hookTimeout), so
// a swim that's exiting doesn't cut one off.
func (h *Hook) Wait() {
	if h == nil {
		return
	}
	h.running.Wait()
}

func (h *Hook) run(n Note) error {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", h.c.Command)
	cmd.Dir = h.c.Root
	// cmd.Environ, not os.Environ: it sets PWD to the root. With swim's own
	// PWD, sh resolves the root's symlinks for $PWD (/private/var/... on
	// macOS), and $PWD no longer matches $SWIM_ROOT.
	cmd.Env = append(cmd.Environ(), n.env(h.c.Root, h.c.URL)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// A child that keeps the output open (sh's curl, once sh is killed)
	// can't hold Wait past the timeout.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("killed after %s", hookTimeout)
	}
	msg := err.Error()
	if s := clean(out.String()); s != "" {
		msg += ": " + Truncate(s, 200)
	}
	return errors.New(msg)
}

// env is n as the hook's environment: one line each, no controls.
func (n Note) env(root, url string) []string {
	lanes := make([]string, len(n.Lanes))
	for i, l := range n.Lanes {
		lanes[i] = strconv.Itoa(l)
	}
	batch := ""
	if n.Batch > 0 {
		batch = strconv.Itoa(n.Batch)
	}
	priority, tags := n.urgency()
	expires := ""
	if !n.Expires.IsZero() {
		expires = n.Expires.Format(time.RFC3339)
	}
	return []string{
		"SWIM_EVENT=" + n.Event,
		"SWIM_RESULT=" + n.Result,
		"SWIM_TITLE=" + clean(n.Title),
		"SWIM_BODY=" + clean(n.Body),
		"SWIM_LANES=" + strings.Join(lanes, ","),
		"SWIM_BATCH=" + batch,
		"SWIM_REPO=" + clean(n.Repo),
		"SWIM_BRANCH=" + clean(n.Branch),
		"SWIM_ROOT=" + root,
		"SWIM_PRIORITY=" + priority,
		"SWIM_TAGS=" + tags,
		"SWIM_NOTIFY_URL=" + url,
		"SWIM_APPROVE_URL=" + n.ApproveURL,
		"SWIM_REJECT_URL=" + n.RejectURL,
		"SWIM_EXPIRES=" + expires,
		"SWIM_ACTIONS=" + n.actions(),
	}
}

// actions is a question's links as ntfy action buttons ("" without
// links): each tap is a POST from the ntfy app on the phone, and clears
// the notification.
func (n Note) actions() string {
	if n.ApproveURL == "" || n.RejectURL == "" {
		return ""
	}
	return "http, Approve, " + n.ApproveURL + ", method=POST, clear=true; http, Decline, " + n.RejectURL + ", method=POST, clear=true"
}

// urgency is n's ntfy priority and tags: an emoji's name, then the repo
// (ntfy shows a tag it has no emoji for as text); high for what needs the
// operator (a question, a failure), default otherwise.
func (n Note) urgency() (priority, tags string) {
	priority, tags = n.level()
	if repo := strings.ReplaceAll(clean(n.Repo), ",", ""); repo != "" {
		tags += "," + repo
	}
	return priority, tags
}

func (n Note) level() (priority, tag string) {
	switch {
	case n.Event == EventAsk:
		return "high", "question"
	case n.Event == EventTest:
		return "default", "bell"
	}
	switch n.Result {
	case "passed":
		return "default", "white_check_mark"
	case "skipped":
		return "default", "pause_button"
	case "interrupted":
		return "high", "stop_sign"
	}
	return "high", "x"
}

// Truncate cuts s to n runes, marking the cut with "…".
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Sent is the record of the last send on this machine (StatePath).
type Sent struct {
	At    time.Time `json:"at"`
	Event string    `json:"event"`
	Title string    `json:"title"`
	Repo  string    `json:"repo"`
	Error string    `json:"error,omitempty"` // "" when it went
}

// StatePath is where the last send is recorded: one file per machine,
// $XDG_STATE_HOME/swim/notify.json (~/.local/state/swim/notify.json).
func StatePath() string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "swim", "notify.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "swim", "notify.json")
}

// LastSent reads the record at path (nil when nothing was sent yet).
func LastSent(path string) (*Sent, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var s Sent
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// record writes how sending n went (the last send wins; best effort: a
// record that can't be written changes nothing sent).
func (h *Hook) record(n Note, err error) {
	if h.c.State == "" {
		return
	}
	s := Sent{At: time.Now().UTC(), Event: n.Event, Title: clean(n.Title), Repo: h.c.Root}
	if err != nil {
		s.Error = err.Error()
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	if os.MkdirAll(filepath.Dir(h.c.State), 0o755) != nil {
		return
	}
	// Written whole, then renamed: swims sending at once never leave half
	// a record.
	tmp, terr := os.CreateTemp(filepath.Dir(h.c.State), ".notify-*.json")
	if terr != nil {
		return
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), h.c.State) != nil {
		os.Remove(tmp.Name())
	}
}
