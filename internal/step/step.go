// Package step runs one command, streaming its output to the terminal
// unchanged while appending a framed, ANSI-stripped copy to the lane log.
package step

import (
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

	"swim/internal/lane"
	"swim/internal/logparse"
	"swim/internal/status"
	"swim/internal/ui"
)

// Options configures one step.
type Options struct {
	Args      []string
	Label     string
	New       bool // truncate the log first
	Snapshot  bool // also save output under .swim/snapshots
	LogPath   string
	Root      string
	Lane      int // 0 when not run from a lane script
	Lanes     int
	HeaderEnv []string
	Runtime   string
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer // wrapper chatter (header/footer) goes here
	Color     bool
}

// Result describes how the step ended.
type Result struct {
	ExitCode int
	Signal   os.Signal // set when interrupted
	Saved    string    // snapshot path, if any
}

// lockedWriter serialises the stdout and stderr copies into one log.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Run executes the step. It returns the command's exit code; a command
// killed by a signal reports 128+signal.
func Run(o Options) (Result, error) {
	if len(o.Args) == 0 {
		return Result{ExitCode: 2}, errors.New("step: no command given (usage: swim step [--label L] -- cmd args...)")
	}
	p := ui.Painter{On: o.Color}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if o.New {
		flags |= os.O_TRUNC
	}
	if dir := filepath.Dir(o.LogPath); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	logf, err := os.OpenFile(o.LogPath, flags, 0o644)
	if err != nil {
		return Result{ExitCode: 1}, fmt.Errorf("open log: %w", err)
	}
	defer logf.Close()

	label := o.Label
	if o.Snapshot && !strings.HasPrefix(label, "snapshot") {
		label = "snapshot: " + label
	}
	cmdline := Quote(o.Args)
	start := time.Now()

	// Header: the full context goes to the log, a short form to the terminal.
	fmt.Fprint(logf, header(o, label, cmdline, start))
	if label != "" {
		fmt.Fprintln(o.Stderr, p.Paint(ui.Bold, "==> "+label))
	}
	fmt.Fprintln(o.Stderr, p.Paint(ui.Dim, "$ "+cmdline))

	var res Result
	var save *os.File
	if o.Snapshot {
		res.Saved = snapshotPath(o.Root, o.Lane, o.Label, start)
		os.MkdirAll(filepath.Dir(res.Saved), 0o755)
		if save, err = os.Create(res.Saved); err != nil {
			return Result{ExitCode: 1}, fmt.Errorf("create snapshot: %w", err)
		}
		defer save.Close()
	}

	if o.Lane > 0 && label != "" {
		status.Update(o.Root, o.Lane, o.Lanes, func(l *status.Lane) { l.CurrentStep = label })
	}

	logSink := io.Writer(logf)
	if save != nil {
		logSink = io.MultiWriter(logf, save)
	}
	plain := &lockedWriter{w: &StripWriter{W: logSink}}

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

	fmt.Fprintln(logf, logparse.OutputMark)
	if err := cmd.Start(); err != nil {
		msg := fmt.Sprintf("swim: cannot start %s: %v\n", o.Args[0], err)
		plain.Write([]byte(msg))
		fmt.Fprint(o.Stderr, msg)
		res.ExitCode = 127 // like the shell: not found
		if errors.Is(err, os.ErrPermission) {
			res.ExitCode = 126
		}
	} else {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
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
			case waitErr = <-done:
				break loop
			}
		}
		res.ExitCode = exitCode(cmd, waitErr)
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			if res.Signal == nil {
				res.Signal = ws.Signal()
			}
		} else if res.Signal != nil && res.ExitCode == 0 {
			// The child handled the signal and succeeded; not an interruption.
			res.Signal = nil
		}
	}

	dur := time.Since(start)
	ds := fmt.Sprintf("%.1fs", dur.Seconds())
	if res.Signal != nil {
		fmt.Fprintf(logf, "%s (signal %s) exit %d (%s)\n", logparse.IntrMark, res.Signal, res.ExitCode, ds)
	} else {
		fmt.Fprintf(logf, "%s %d (%s)\n", logparse.ExitMark, res.ExitCode, ds)
	}

	if label != "" {
		kind, detail := logparse.Pass, ""
		if res.ExitCode != 0 {
			kind, detail = logparse.Fail, fmt.Sprintf("exit %d", res.ExitCode)
			if res.Signal != nil {
				detail += ", interrupted"
			}
		}
		line := logparse.ResultLine(kind, label, detail)
		fmt.Fprintln(logf, line)
		fmt.Fprintln(o.Stderr, p.Paint(ui.StateColor(kind), line)+p.Paint(ui.Dim, "  "+ds))
		if res.Saved != "" {
			fmt.Fprintf(logf, "saved: %s\n", rel(o.Root, res.Saved))
			fmt.Fprintln(o.Stderr, p.Paint(ui.Dim, "saved: "+rel(o.Root, res.Saved)))
		}
		if o.Lane > 0 {
			status.Update(o.Root, o.Lane, o.Lanes, func(l *status.Lane) {
				if kind == logparse.Pass {
					l.Pass++
				} else {
					l.Fail++
					l.FailedSteps = append(l.FailedSteps, line)
				}
			})
		}
	} else {
		fmt.Fprintln(o.Stderr, p.Paint(ui.Dim, fmt.Sprintf("exit %d  %s", res.ExitCode, ds)))
	}
	fmt.Fprintln(logf)
	return res, nil
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

func header(o Options, label, cmdline string, start time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", logparse.StepStart, start.UTC().Format(time.RFC3339), label)
	fmt.Fprintf(&b, "$ %s\n", cmdline)
	cwd, _ := os.Getwd()
	fmt.Fprintf(&b, "cwd: %s   git: %s", cwd, GitRef(cwd))
	if o.Runtime != "" {
		fmt.Fprintf(&b, "   runtime: %s", runtimeVersion(o.Runtime))
	}
	if job := os.Getenv("SWIM_JOB"); job != "" {
		fmt.Fprintf(&b, "   job: %s", job)
	}
	b.WriteString("\n")
	if env := envLine(o.HeaderEnv); env != "" {
		fmt.Fprintf(&b, "env: %s\n", env)
	}
	return b.String()
}

// envLine records the cloud profile and configured keys. Only names listed
// in config are printed, so secrets stay out of logs unless someone lists one.
func envLine(keys []string) string {
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

func runtimeVersion(cmdline string) string {
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
