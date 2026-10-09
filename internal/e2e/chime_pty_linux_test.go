package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair with only the standard library
// (Linux /dev/ptmx). It returns the controlling side and the terminal.
func openPTY(t *testing.T) (ptm, pts *os.File) {
	t.Helper()
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no /dev/ptmx:", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptm.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		ptm.Close()
		t.Skip("unlockpt:", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, ptm.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		ptm.Close()
		t.Skip("ptsname:", e)
	}
	pts, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ptm.Close()
		t.Skip("open pts:", err)
	}
	return ptm, pts
}

// swimTTY runs swim with stdout and stderr on a terminal and returns
// everything it wrote and its exit code.
func (r *repo) swimTTY(args ...string) (string, int) {
	r.t.Helper()
	ptm, pts := openPTY(r.t)
	defer ptm.Close()
	c := r.cmd(bin, args...)
	c.Stdout, c.Stderr = pts, pts
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	pts.Close()
	var out bytes.Buffer
	read := make(chan struct{})
	go func() {
		io.Copy(&out, ptm) // ends with EIO once swim exits
		close(read)
	}()
	err := c.Wait()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		r.t.Fatal("pty output never ended")
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	return out.String(), code
}

// Under a terminal the bell ends the output: once for a pass, twice for a
// failure, never when quiet by config, flag, duration or CI.
func TestChimeOnTerminal(t *testing.T) {
	r := newRepo(t, "")
	r.env = append(r.env, "CI=")
	r.script(1, "passes", `run "ok" true`)
	r.script(2, "fails", `run "nope" false`)

	quiet := func(what, out string) {
		t.Helper()
		if strings.Contains(out, "\a") {
			t.Errorf("%s: unexpected bell: %q", what, tail(out))
		}
	}
	// Off by default.
	out, _ := r.swimTTY("run", "1", "--plain")
	contains(t, "tty run", out, "swim summary")
	quiet("default config", out)

	r.mustSwim("config", "chime", "true")
	quiet("below chime_min_s", must(r.swimTTY("run", "1", "--plain")))
	r.mustSwim("config", "chime_min_s", "0")

	out = must(r.swimTTY("run", "1", "--plain"))
	if !strings.HasSuffix(out, "\a") || strings.HasSuffix(out, "\a\a") {
		t.Errorf("chime true, pass: want one trailing bell: %q", tail(out))
	}
	quiet("--no-chime", must(r.swimTTY("run", "1", "--plain", "--no-chime")))

	r.mustSwim("config", "chime", "failure")
	quiet("failure mode, pass", must(r.swimTTY("run", "1", "--plain")))
	out, code := r.swimTTY("run", "2", "--plain")
	if code != 1 || !strings.HasSuffix(out, "\a\a") {
		t.Errorf("failure mode, fail: exit %d, want two trailing bells: %q", code, tail(out))
	}
	// The live view (not --plain) too: the bell comes after the summary.
	out, _ = r.swimTTY("run", "2")
	if !strings.HasSuffix(out, "\a\a") || strings.Index(out, "\a") < strings.LastIndex(out, "swim summary") {
		t.Errorf("live view: %q", tail(out))
	}

	r.mustSwim("config", "chime", "false")
	out = must(r.swimTTY("run", "1", "--plain", "--chime"))
	if !strings.HasSuffix(out, "\a") {
		t.Errorf("--chime over chime false: %q", tail(out))
	}

	r.env = append(r.env, "CI=true")
	quiet("CI", must(r.swimTTY("run", "1", "--plain", "--chime")))
}

func must(out string, _ int) string { return out }

func tail(s string) string {
	if len(s) > 200 {
		return "…" + s[len(s)-200:]
	}
	return s
}
