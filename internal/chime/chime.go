// Package chime rings the terminal bell, plays a sound or shows a desktop
// notification when swim run / swim all finishes (features/chime.md).
// Should is the pure decision; Chimer does the ringing, with its output
// writer and external-command runner injectable so tests never make noise.
package chime

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
)

// Result is how the run ended.
type Result int

const (
	Passed      Result = iota // every lane passed
	Failed                    // a lane failed or was skipped
	Interrupted               // Ctrl-C or a signal stopped the run
)

// Timeout bounds an external sound or notification command.
const Timeout = 2 * time.Second

// Input is everything the chime decision depends on.
type Input struct {
	Mode    config.Chime // on | off | failure
	Style   string       // bell | sound | notify
	MinS    int          // runs shorter than this stay quiet
	Result  Result
	Elapsed time.Duration
	TTY     bool // stdout is a terminal
	CI      bool // CI=true
}

// Should reports whether a finished run chimes: chime is on (or failure
// and the run didn't fully pass), the run lasted at least MinS seconds,
// stdout is a terminal unless the style is notify, and it isn't CI.
func Should(in Input) bool {
	if in.CI {
		return false
	}
	switch in.Mode {
	case config.ChimeOn:
	case config.ChimeFailure:
		if in.Result == Passed {
			return false
		}
	default:
		return false
	}
	if in.Elapsed < time.Duration(in.MinS)*time.Second {
		return false
	}
	return in.TTY || in.Style == config.StyleNotify
}

// IsCI reports whether the environment says this is a CI run (CI=true).
func IsCI(getenv func(string) string) bool {
	v := strings.ToLower(strings.TrimSpace(getenv("CI")))
	return v == "true" || v == "1"
}

// Message is the notification text, e.g. "3 passed, 1 failed (0:42)".
func Message(passed, failed, skipped, interrupted int, elapsed string) string {
	parts := []string{fmt.Sprintf("%d passed", passed)}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	if interrupted > 0 {
		parts = append(parts, fmt.Sprintf("%d interrupted", interrupted))
	}
	return fmt.Sprintf("%s (%s)", strings.Join(parts, ", "), elapsed)
}

// Chimer rings. The zero value of each field is replaced by the real thing
// in New; tests set their own.
type Chimer struct {
	Out      io.Writer // where the bell goes
	TTY      bool      // Out is a terminal (no bell into pipes or files)
	GOOS     string
	LookPath func(string) (string, error)
	// Start launches an external command without waiting for it. Its error
	// is ignored: a chime never changes the exit code.
	Start func(name string, args ...string) error
}

// New returns a Chimer that writes to out and runs real commands detached.
func New(out *os.File, tty bool) Chimer {
	return Chimer{Out: out, TTY: tty, GOOS: runtime.GOOS, LookPath: exec.LookPath, Start: StartDetached}
}

// Ring chimes once in style: the bell (twice when failed), plus for sound
// or notify an external command when one is available.
func (c Chimer) Ring(style string, failed bool, message string) {
	if c.TTY && c.Out != nil {
		bell := "\a"
		if failed {
			bell = "\a\a"
		}
		io.WriteString(c.Out, bell)
	}
	var cmd []string
	switch style {
	case config.StyleSound:
		cmd = c.soundCommand(failed)
	case config.StyleNotify:
		cmd = c.notifyCommand(message)
	}
	if len(cmd) > 0 && c.Start != nil {
		_ = c.Start(cmd[0], cmd[1:]...)
	}
}

func (c Chimer) has(name string) bool {
	if c.LookPath == nil {
		return false
	}
	_, err := c.LookPath(name)
	return err == nil
}

// soundCommand picks the platform's sound player, or nil (bell only).
func (c Chimer) soundCommand(failed bool) []string {
	switch c.GOOS {
	case "darwin":
		if !c.has("afplay") {
			return nil
		}
		sound := "Glass"
		if failed {
			sound = "Basso"
		}
		return []string{"afplay", "/System/Library/Sounds/" + sound + ".aiff"}
	case "linux":
		id := "complete"
		if failed {
			id = "dialog-error"
		}
		if c.has("canberra-gtk-play") {
			return []string{"canberra-gtk-play", "-i", id}
		}
		if c.has("paplay") {
			return []string{"paplay", "/usr/share/sounds/freedesktop/stereo/" + id + ".oga"}
		}
	}
	return nil
}

// notifyCommand picks the platform's notifier, or nil (bell only).
func (c Chimer) notifyCommand(message string) []string {
	switch c.GOOS {
	case "darwin":
		if !c.has("osascript") {
			return nil
		}
		return []string{"osascript", "-e", fmt.Sprintf("display notification %s with title %s", appleString(message), appleString("swim"))}
	case "linux":
		if c.has("notify-send") {
			return []string{"notify-send", "swim", message}
		}
	}
	return nil
}

// appleString quotes s as an AppleScript string literal.
func appleString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// StartDetached starts a command in its own process group with no stdio,
// and kills it after Timeout if swim is still running. swim never waits
// for it, so it can't delay exit.
func StartDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		t := time.AfterFunc(Timeout, func() { _ = cmd.Process.Kill() })
		_ = cmd.Wait()
		t.Stop()
	}()
	return nil
}
