package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Events a Note is about (SWIM_EVENT).
const (
	EventAsk = "ask" // a lane waits on the operator
	EventEnd = "end" // a run or a batch ended
)

// Note is one notification, as a hook gets it: what the terminal shows
// (Title, Body) and what a script would branch on.
type Note struct {
	Event  string // EventAsk or EventEnd
	Result string // for EventEnd: passed, failed, interrupted, skipped (a guard dry-ran) or error
	Title  string
	Body   string
	Lanes  []int
	Batch  int // swim watch's batch number (0: a swim run)
}

// hookTimeout is how long one hook command may take before it's killed.
const hookTimeout = 15 * time.Second

// Hook runs the operator's notify_command for each Note: `sh -c COMMAND` in
// the repo root, with the Note in its environment (SWIM_EVENT, SWIM_RESULT,
// SWIM_TITLE, SWIM_BODY, SWIM_LANES, SWIM_BATCH, SWIM_ROOT), so it can send
// the note anywhere (curl to ntfy, a webhook, say). Commands run in the
// background; Wait waits for them. A nil Hook runs nothing.
type Hook struct {
	cmd, root string
	warn      func(string) // a command that failed, and why
	running   sync.WaitGroup
}

// NewHook returns the Hook for command in root (nil: no command). warn
// hears of a command that failed or timed out (nil: nobody).
func NewHook(command, root string, warn func(string)) *Hook {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return &Hook{cmd: command, root: root, warn: warn}
}

// Send starts the command for n and returns at once.
func (h *Hook) Send(n Note) {
	if h == nil {
		return
	}
	h.running.Add(1)
	go func() {
		defer h.running.Done()
		if err := h.run(n); err != nil && h.warn != nil {
			h.warn(err.Error())
		}
	}()
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
	cmd := exec.CommandContext(ctx, "sh", "-c", h.cmd)
	cmd.Dir = h.root
	cmd.Env = append(os.Environ(), n.env(h.root)...)
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
	msg := "notify_command: " + err.Error()
	if s := clean(out.String()); s != "" {
		msg += ": " + truncate(s, 200)
	}
	return errors.New(msg)
}

// env is n as the hook's environment: one line each, no controls.
func (n Note) env(root string) []string {
	lanes := make([]string, len(n.Lanes))
	for i, l := range n.Lanes {
		lanes[i] = strconv.Itoa(l)
	}
	batch := ""
	if n.Batch > 0 {
		batch = strconv.Itoa(n.Batch)
	}
	return []string{
		"SWIM_EVENT=" + n.Event,
		"SWIM_RESULT=" + n.Result,
		"SWIM_TITLE=" + clean(n.Title),
		"SWIM_BODY=" + clean(n.Body),
		"SWIM_LANES=" + strings.Join(lanes, ","),
		"SWIM_BATCH=" + batch,
		"SWIM_ROOT=" + root,
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
