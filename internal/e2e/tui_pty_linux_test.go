package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"
)

// The interactive view under a pseudo-terminal: swim runs as the session
// leader with the terminal as its controlling tty (as from a shell), the
// test types keys into it and reads the screen back through a small
// terminal emulator (just what the view draws: cursor moves, erases, the
// alternate screen).

const ptyRows, ptyCols = 30, 100

// tuiRun is a swim process on a terminal.
type tuiRun struct {
	t      *testing.T
	ptm    *os.File
	cmd    *exec.Cmd
	mu     sync.Mutex
	raw    bytes.Buffer // everything swim wrote
	read   chan struct{}
	before termios
	exit   chan error
}

type termios struct{ Iflag, Oflag, Cflag, Lflag uint32 }

func getTermios(t *testing.T, f *os.File) termios {
	t.Helper()
	var tio syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&tio))); e != 0 {
		t.Fatal("TCGETS:", e)
	}
	return termios{tio.Iflag, tio.Oflag, tio.Cflag, tio.Lflag}
}

// startTUI runs swim on a fresh terminal with colour and the view allowed.
func (r *repo) startTUI(env []string, args ...string) *tuiRun {
	r.t.Helper()
	ptm, pts := openPTY(r.t)
	ws := struct{ rows, cols, x, y uint16 }{ptyRows, ptyCols, 0, 0}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, pts.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		r.t.Fatal("TIOCSWINSZ:", e)
	}
	c := r.cmd(bin, args...)
	c.Env = append(c.Env, "NO_COLOR=", "SWIM_COLOR=", "CI=", "SWIM_TUI=", "TERM=xterm")
	c.Env = append(c.Env, env...)
	c.Stdin, c.Stdout, c.Stderr = pts, pts, pts
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	tr := &tuiRun{t: r.t, ptm: ptm, cmd: c, read: make(chan struct{}), exit: make(chan error, 1)}
	tr.before = getTermios(r.t, pts)
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	pts.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptm.Read(buf)
			tr.mu.Lock()
			tr.raw.Write(buf[:n])
			tr.mu.Unlock()
			if err != nil {
				close(tr.read)
				return
			}
		}
	}()
	go func() { tr.exit <- c.Wait() }()
	r.t.Cleanup(func() {
		c.Process.Kill()
		ptm.Close()
	})
	return tr
}

func (tr *tuiRun) output() string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return tr.raw.String()
}

// screen is what the alternate screen shows now.
func (tr *tuiRun) screen() string { return emulate(tr.output(), ptyRows, ptyCols) }

func (tr *tuiRun) send(keys string) {
	tr.t.Helper()
	if _, err := tr.ptm.Write([]byte(keys)); err != nil {
		tr.t.Fatal(err)
	}
}

// waitScreen waits until the screen satisfies ok.
func (tr *tuiRun) waitScreen(what string, ok func(string) bool) string {
	tr.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		s := tr.screen()
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			tr.t.Fatalf("timed out waiting for %s; screen:\n%s", what, s)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func (tr *tuiRun) waitFor(want string) string {
	tr.t.Helper()
	return tr.waitScreen(fmt.Sprintf("%q", want), func(s string) bool { return strings.Contains(s, want) })
}

// wait waits for swim to exit; it returns the exit code and everything
// written.
func (tr *tuiRun) wait() (int, string) {
	tr.t.Helper()
	var err error
	select {
	case err = <-tr.exit:
	case <-time.After(20 * time.Second):
		tr.t.Fatalf("swim never exited; screen:\n%s", tr.screen())
	}
	// The terminal's settings outlive swim while the test holds the pty.
	after := tr.termiosNow()
	if after != tr.before {
		tr.t.Errorf("terminal not restored: before %+v, after %+v", tr.before, after)
	}
	select {
	case <-tr.read:
	case <-time.After(5 * time.Second):
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		tr.t.Fatal(err)
	}
	return code, tr.output()
}

func (tr *tuiRun) termiosNow() termios { return getTermios(tr.t, tr.ptm) }

// emulate replays out on a rows×cols terminal and returns the alternate
// screen if it is showing, else the main screen's last rows.
func emulate(out string, rows, cols int) string {
	type grid [][]rune
	blank := func() grid {
		g := make(grid, rows)
		for i := range g {
			g[i] = []rune(strings.Repeat(" ", cols))
		}
		return g
	}
	main, alt := blank(), blank()
	g := main
	r, c := 0, 0
	for i := 0; i < len(out); {
		ch := out[i]
		switch {
		case ch == 0x1b && i+1 < len(out) && out[i+1] == '[':
			j := i + 2
			for j < len(out) && (out[j] < 0x40 || out[j] > 0x7e) {
				j++
			}
			if j >= len(out) {
				i = len(out)
				continue
			}
			params, fin := out[i+2:j], out[j]
			i = j + 1
			switch {
			case fin == 'H':
				r, c = 0, 0
				if p := strings.Split(params, ";"); len(p) == 2 {
					r, _ = strconv.Atoi(p[0])
					c, _ = strconv.Atoi(p[1])
					r, c = r-1, c-1
				}
			case fin == 'K':
				for x := c; x < cols && r < rows; x++ {
					g[r][x] = ' '
				}
			case fin == 'J' && params == "2":
				for y := range g {
					g[y] = []rune(strings.Repeat(" ", cols))
				}
			case fin == 'h' && params == "?1049":
				alt = blank()
				g = alt
			case fin == 'l' && params == "?1049":
				g = main
			}
		case ch == 0x1b:
			i += 2
		case ch == '\r':
			c = 0
			i++
		case ch == '\n':
			if r < rows-1 {
				r++
			} else if !sameGrid(g, alt) {
				g = append(g[1:], []rune(strings.Repeat(" ", cols)))
				main = g
			}
			i++
		case ch < 0x20:
			i++
		default:
			ru, size := utf8.DecodeRuneInString(out[i:])
			i += size
			if r >= 0 && r < rows && c >= 0 && c < cols {
				g[r][c] = ru
			}
			c++
		}
	}
	var b strings.Builder
	for _, line := range g {
		b.WriteString(strings.TrimRight(string(line), " ") + "\n")
	}
	return b.String()
}

func sameGrid(a, b [][]rune) bool { return len(a) > 0 && len(b) > 0 && &a[0] == &b[0] }

// tuiLanes writes lanes that print and then wait for the release file, so
// the run stays up while the test drives the view.
func tuiLanes(r *repo, n int) string {
	release := filepath.Join(r.root, "release")
	for i := 1; i <= n; i++ {
		r.script(i, fmt.Sprintf("tui lane %d", i), fmt.Sprintf(
			`run "talk" bash -c 'for i in $(seq 1 %d); do echo lane%d-line-$i; done; echo lane%d-hello'
run "hold" bash -c 'while [ ! -f %s ]; do sleep 0.05; done'`, 5+30*(i%2), i, i, release))
	}
	return release
}

func TestTUILaneViewAndCtrlC(t *testing.T) {
	r := newRepo(t, "")
	release := tuiLanes(r, 3)
	tr := r.startTUI(nil, "all")
	s := tr.waitScreen("all view with lane output", func(s string) bool {
		return strings.Contains(s, "all lanes · following") && strings.Contains(s, "] ==> hold")
	})
	contains(t, "all view", s, "swim 1", "swim 3", "← → lane")

	// → opens the first lane in panel order; → again moves to lane 2.
	tr.send("\x1b[C")
	tr.waitFor("swim 1 · following · .swim/logs/agent1.log")
	tr.send("l")
	s = tr.waitScreen("lane 2's log", func(s string) bool {
		return strings.Contains(s, "swim 2 · following") && strings.Contains(s, "| lane2-hello")
	})
	contains(t, "lane view", s, "== ROUND ", "PASS  talk", "▶swim 2")
	for _, bad := range []string{"[1]", "[3]", "lane1-", "lane3-"} {
		if strings.Contains(s, bad) {
			t.Errorf("lane 2's view shows %q:\n%s", bad, s)
		}
	}
	// Digits then Enter jump; Esc returns to the all view.
	tr.send("3\r")
	tr.waitFor("swim 3 · following")
	tr.send("\x1b")
	tr.waitFor("all lanes · following")

	// ↑ pauses the combined output; End follows again.
	tr.send("\x1b[A")
	tr.waitFor("all lanes · paused")
	tr.send("\x1b[F")
	tr.waitFor("all lanes · following")
	tr.send("?")
	tr.waitFor("any key closes this help")
	tr.send("x")
	tr.waitScreen("help closed", func(s string) bool { return !strings.Contains(s, "any key closes this help") })

	// Ctrl-C goes to the same escalation as without the view: the first
	// press stops the lanes once their running step ("hold") returns.
	tr.send("\x03")
	tr.waitScreen("lanes stopping", func(s string) bool {
		return strings.Contains(s, "stopping · 3 steps running") && strings.Contains(s, "stopping hold")
	})
	os.WriteFile(release, nil, 0o644)
	code, out := tr.wait()
	if code == 0 {
		t.Errorf("interrupted run exited 0")
	}
	end := out[strings.LastIndex(out, "\x1b[?1049l"):]
	contains(t, "after the view", end, "swim summary", "INTERRUPTED")
}

func TestTUIHoldsOpenAfterRun(t *testing.T) {
	r := newRepo(t, "")
	release := tuiLanes(r, 2)
	tr := r.startTUI(nil, "all")
	tr.waitFor("all lanes · following")
	tr.send("2\r")
	tr.waitFor("swim 2 · following")
	os.WriteFile(release, nil, 0o644)
	tr.waitScreen("the finished lane's END", func(s string) bool {
		return strings.Contains(s, "run finished · q to close") && strings.Contains(s, "== END PASS")
	})
	tr.send("q")
	code, out := tr.wait()
	if code != 0 {
		t.Errorf("exit %d", code)
	}
	contains(t, "summary", out[strings.LastIndex(out, "\x1b[?1049l"):], "swim summary", "PASS")

	// Following the all view at the end, it closes by itself.
	tr = r.startTUI(nil, "all", "--rerun")
	tr.waitFor("all lanes · following")
	code, out = tr.wait()
	if code != 0 || !strings.Contains(out, "\x1b[?1049h") {
		t.Errorf("rerun: exit %d, view used: %v", code, strings.Contains(out, "\x1b[?1049h"))
	}
}

// SIGTERM, SIGHUP and a panic in the view all leave the terminal as it was.
func TestTUIRestoresTerminal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			r := newRepo(t, "")
			release := tuiLanes(r, 2)
			tr := r.startTUI(nil, "all")
			tr.waitFor("all lanes · following")
			tr.send("1\r") // a lane view would hold the screen at the end; a signal must not
			tr.waitFor("swim 1 · following")
			tr.cmd.Process.Signal(sig)
			// The waiting command doesn't get the signal (swim passes it to
			// the lane scripts only), so let the lanes finish.
			time.Sleep(200 * time.Millisecond)
			os.WriteFile(release, nil, 0o644)
			code, out := tr.wait()
			if !strings.Contains(out, "\x1b[?1049l") {
				t.Errorf("exit %d, left the alternate screen: %v", code, strings.Contains(out, "\x1b[?1049l"))
			}
		})
	}
	t.Run("panic", func(t *testing.T) {
		r := newRepo(t, "")
		release := tuiLanes(r, 2)
		defer os.WriteFile(release, nil, 0o644) // let the orphaned lanes end
		tr := r.startTUI([]string{"SWIM_TEST_TUI_PANIC=1"}, "all")
		tr.waitFor("all lanes · following")
		tr.send("1\r")
		select {
		case err := <-tr.exit:
			tr.exit <- err
		case <-time.After(15 * time.Second):
			t.Fatal("no panic")
		}
		after := tr.termiosNow()
		if after != tr.before {
			t.Errorf("terminal not restored after a panic: before %+v, after %+v", tr.before, after)
		}
		time.Sleep(100 * time.Millisecond)
		if out := tr.output(); !strings.Contains(out, "\x1b[?1049l") || !strings.Contains(out, "SWIM_TEST_TUI_PANIC") {
			t.Errorf("panic output: %q", tail(out))
		}
	})
}

// The existing views when the interactive one is ruled out.
func TestTUIOff(t *testing.T) {
	r := newRepo(t, "")
	release := tuiLanes(r, 2)
	os.WriteFile(release, nil, 0o644)
	for _, c := range []struct {
		name string
		env  []string
		args []string
	}{
		{"--no-tui", nil, []string{"all", "--rerun", "--no-tui"}},
		{"SWIM_TUI=0", []string{"SWIM_TUI=0"}, []string{"all", "--rerun"}},
		{"CI", []string{"CI=true"}, []string{"all", "--rerun"}},
		{"--plain", nil, []string{"all", "--rerun", "--plain"}},
		{"NO_COLOR", []string{"NO_COLOR=1"}, []string{"all", "--rerun"}},
		{"one lane", nil, []string{"run", "1"}},
	} {
		tr := r.startTUI(c.env, c.args...)
		code, out := tr.wait()
		if code != 0 || strings.Contains(out, "\x1b[?1049h") || !strings.Contains(out, "swim summary") {
			t.Errorf("%s: exit %d, used the interactive view: %v\n%s", c.name, code, strings.Contains(out, "\x1b[?1049h"), tail(out))
		}
	}
}

// With more lanes than the panel shows, the selected lane keeps a row.
func TestTUIManyLanesKeepsSelection(t *testing.T) {
	r := newRepo(t, "")
	r.mustSwim("config", "lanes", "40")
	release := filepath.Join(r.root, "release")
	for i := 1; i <= 40; i++ {
		body := `run "quick" true`
		if i == 1 {
			body = fmt.Sprintf(`run "hold" bash -c 'while [ ! -f %s ]; do sleep 0.05; done'`, release)
		}
		r.script(i, fmt.Sprintf("many %d", i), body)
	}
	tr := r.startTUI(nil, "all")
	tr.waitFor("more:")
	tr.send("35\r")
	s := tr.waitFor("swim 35 · ")
	contains(t, "panel", s, "▶swim 35", "swim 1 ")
	tr.send("\x1b")
	tr.waitFor("all lanes · following")
	os.WriteFile(release, nil, 0o644)
	if code, _ := tr.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
}
