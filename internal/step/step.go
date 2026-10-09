// Package step runs one command, streaming its output to the terminal
// unchanged and writing it, ANSI-stripped and framed, to the lane log.
package step

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Options configures one step.
type Options struct {
	Args     []string
	Label    string
	New      bool // start a fresh log
	Snapshot bool // also save output under .swim/snapshots
	LogPath  string
	Root     string
	Lane     int // 0 when not run from a lane script
	Lanes    int
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer // wrapper chatter (command and result) goes here
	Color    bool
	// Deadline, when set, is the round's time limit (the script's Timeout:):
	// a step still running then is stopped; past it, steps don't start.
	Deadline    time.Time
	TimeoutText string // the Timeout: value, for messages
}

// Result describes how the step ended.
type Result struct {
	ExitCode int
	Signal   os.Signal // set when interrupted
	TimedOut bool      // stopped (or not started) by the round's Timeout
	Saved    string    // snapshot path, if any
}

// lockedWriter serialises the stdout and stderr copies into one spool.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// SpoolPath is where a running step's output collects before its block is
// written to the log (the result line has to come first).
func SpoolPath(logPath string) string { return logPath + ".partial" }

const spoolMagic = "#swim-spool"

// Run executes the step. Output streams to the terminal unchanged and,
// ANSI-stripped, to a spool file; when the command ends, the step's block
// (result line, command, output) is appended to the log. It returns the
// command's exit code; a command killed by a signal reports 128+signal, and
// one stopped by the round's Timeout reports 124.
func Run(o Options) (Result, error) {
	if len(o.Args) == 0 {
		return Result{ExitCode: 2}, errors.New("step: no command given (usage: swim step [--label L] -- cmd args...)")
	}
	p := ui.Painter{On: o.Color}
	if dir := filepath.Dir(o.LogPath); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	if o.New {
		os.Remove(o.LogPath)
	}
	if err := logparse.EnsureV2(o.LogPath, o.Lane); err != nil {
		return Result{ExitCode: 1}, err
	}
	RecoverSpool(o.LogPath)

	label := o.Label
	if o.Snapshot && !strings.HasPrefix(label, "snapshot") {
		label = "snapshot: " + label
	}
	cmdline := Quote(o.Args)
	logLabel := label
	if logLabel == "" {
		logLabel = shorten(cmdline, 40)
	}
	start := time.Now()
	clock := start.Format("15:04:05")

	if label != "" {
		fmt.Fprintln(o.Stderr, p.Paint(ui.Bold, "==> "+label))
	}
	fmt.Fprintln(o.Stderr, p.Paint(ui.Dim, "$ "+cmdline))

	var res Result
	var err error
	var save *os.File
	if o.Snapshot {
		res.Saved = snapshotPath(o.Root, o.Lane, o.Label, start)
		os.MkdirAll(filepath.Dir(res.Saved), 0o755)
		if save, err = os.Create(res.Saved); err != nil {
			return Result{ExitCode: 1}, fmt.Errorf("create snapshot: %w", err)
		}
		defer save.Close()
	}
	spool, err := os.Create(SpoolPath(o.LogPath))
	if err != nil {
		return Result{ExitCode: 1}, fmt.Errorf("open spool: %w", err)
	}
	fmt.Fprintf(spool, "%s\t%s\t%s\t%s\n", spoolMagic, logLabel, clock, cmdline)

	if o.Lane > 0 && label != "" {
		status.Update(o.Root, o.Lane, o.Lanes, func(l *status.Lane) { l.CurrentStep = label })
	}

	sink := io.Writer(spool)
	if save != nil {
		sink = io.MultiWriter(spool, save)
	}
	plain := &lockedWriter{w: &StripWriter{W: sink}}

	cmd := exec.Command(o.Args[0], o.Args[1:]...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = io.MultiWriter(o.Stdout, plain)
	cmd.Stderr = io.MultiWriter(o.Stderr, plain)
	cmd.WaitDelay = 2 * time.Second // don't hang on grandchildren holding the pipes

	// The terminal delivers Ctrl-C to the whole process group, so the child
	// already has it: we only note it, wait, and flush. SIGTERM/SIGHUP may be
	// aimed at us alone, so those are forwarded.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	switch {
	case !o.Deadline.IsZero() && !start.Before(o.Deadline):
		res.TimedOut = true
		msg := fmt.Sprintf("swim: not started: the round's time limit (Timeout: %s) has run out\n", o.TimeoutText)
		plain.Write([]byte(msg))
		fmt.Fprint(o.Stderr, msg)
	default:
		if startErr := cmd.Start(); startErr != nil {
			msg := fmt.Sprintf("swim: cannot start %s: %v\n", o.Args[0], startErr)
			plain.Write([]byte(msg))
			fmt.Fprint(o.Stderr, msg)
			res.ExitCode = 127 // like the shell: not found
			if errors.Is(startErr, os.ErrPermission) {
				res.ExitCode = 126
			}
			break
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var deadline, kill <-chan time.Time
		if !o.Deadline.IsZero() {
			t := time.NewTimer(time.Until(o.Deadline))
			defer t.Stop()
			deadline = t.C
		}
		var waitErr error
	loop:
		for {
			select {
			case s := <-sigs:
				if res.Signal == nil {
					res.Signal = s
				}
				if s != syscall.SIGINT && cmd.Process != nil {
					cmd.Process.Signal(s)
				}
			case <-deadline:
				res.TimedOut = true
				msg := fmt.Sprintf("swim: stopping: the round's time limit (Timeout: %s) ran out\n", o.TimeoutText)
				plain.Write([]byte(msg))
				fmt.Fprint(o.Stderr, msg)
				cmd.Process.Signal(syscall.SIGTERM)
				kill = time.After(5 * time.Second)
			case <-kill:
				cmd.Process.Kill()
			case waitErr = <-done:
				break loop
			}
		}
		res.ExitCode = exitCode(cmd, waitErr)
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() && !res.TimedOut {
			if res.Signal == nil {
				res.Signal = ws.Signal()
			}
		} else if res.Signal != nil && res.ExitCode == 0 {
			// The child handled the signal and succeeded; not an interruption.
			res.Signal = nil
		}
	}
	if res.TimedOut {
		res.ExitCode, res.Signal = 124, nil
	}
	spool.Close()

	dur := time.Since(start)
	ds := fmt.Sprintf("%.1fs", dur.Seconds())
	kind, detail := logparse.Pass, ""
	switch {
	case res.TimedOut:
		kind, detail = logparse.Fail, "timeout: Timeout "+o.TimeoutText+" reached"
	case res.ExitCode != 0:
		kind, detail = logparse.Fail, fmt.Sprintf("exit %d", res.ExitCode)
		if res.Signal != nil {
			detail += ", interrupted"
		}
	}
	saved := ""
	if res.Saved != "" {
		saved = rel(o.Root, res.Saved)
	}
	if err := writeBlock(o.LogPath, logparse.StepLine(kind, logLabel, detail, ds, clock), saved); err != nil {
		return res, err
	}

	line := logparse.ResultLine(kind, logLabel, detail)
	fmt.Fprintln(o.Stderr, p.Paint(ui.StateColor(kind), line)+p.Paint(ui.Dim, "  "+ds))
	if saved != "" {
		fmt.Fprintln(o.Stderr, p.Paint(ui.Dim, "saved: "+saved))
	}
	if o.Lane > 0 && label != "" {
		status.Update(o.Root, o.Lane, o.Lanes, func(l *status.Lane) {
			if kind == logparse.Pass {
				l.Pass++
			} else {
				l.Fail++
				l.FailedSteps = append(l.FailedSteps, line)
			}
		})
	}
	return res, nil
}

// writeBlock appends a step's block to the log: its result line, then the
// command and output from the spool, then the snapshot path. The spool is
// removed afterwards.
func writeBlock(logPath, resultLine, saved string) error {
	sp := SpoolPath(logPath)
	in, err := os.Open(sp)
	if err != nil {
		return err
	}
	defer os.Remove(sp)
	defer in.Close()
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	w := bufio.NewWriterSize(out, 64*1024)
	w.WriteString(resultLine + "\n")
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if f := strings.SplitN(line, "\t", 4); len(f) == 4 && f[0] == spoolMagic {
				w.WriteString(logparse.CmdPrefix + f[3] + "\n")
				continue
			}
		}
		w.WriteString(logparse.OutPrefix + line + "\n")
	}
	if saved != "" {
		w.WriteString(logparse.Indent + "saved: " + saved + "\n")
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return sc.Err()
}

// RecoverSpool writes a step that never finished (swim step was killed
// outright, so it never wrote its block) into the log as a failed step with
// whatever output it had collected.
func RecoverSpool(logPath string) {
	sp := SpoolPath(logPath)
	f, err := os.Open(sp)
	if err != nil {
		return
	}
	label, clock := "(unknown step)", "--:--:--"
	if line, err := bufio.NewReader(f).ReadString('\n'); err == nil {
		if p := strings.SplitN(strings.TrimRight(line, "\n"), "\t", 4); len(p) == 4 && p[0] == spoolMagic {
			label, clock = p[1], p[2]
		}
	}
	f.Close()
	writeBlock(logPath, logparse.StepLine(logparse.Fail, label, "cut off: swim step was killed, output recovered", "?", clock), "")
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return 1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	if err != nil {
		return 1
	}
	return 0
}

// EnvLine records the cloud profile and configured keys. Only names listed
// in config are printed, so secrets stay out of logs unless someone lists one.
func EnvLine(keys []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, k := range append([]string{"AWS_PROFILE"}, keys...) {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		v, ok := os.LookupEnv(k)
		if !ok {
			if k == "AWS_PROFILE" {
				continue
			}
			v = "<unset>"
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

// GitRef returns branch@shortsha for dir, or "-" outside git.
func GitRef(dir string) string {
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	sha := run("rev-parse", "--short", "HEAD")
	if sha == "" {
		return "-"
	}
	branch := run("rev-parse", "--abbrev-ref", "HEAD")
	return branch + "@" + sha
}

// RuntimeVersion runs cmdline (config's runtime:) and returns its first line.
func RuntimeVersion(cmdline string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", cmdline).CombinedOutput()
	v := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if err != nil && v == "" {
		return "unknown (" + cmdline + " failed)"
	}
	return v
}

func snapshotPath(root string, n int, label string, t time.Time) string {
	slug := lane.Slug(label)
	if slug == "" {
		slug = "snapshot"
	}
	return filepath.Join(lane.SnapshotDir(root), fmt.Sprintf("lane%d-%s-%s.txt", n, t.UTC().Format("20060102T150405Z"), slug))
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}
