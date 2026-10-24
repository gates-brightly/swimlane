package launcher

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/status"
)

// Reaped is a round ReapDead closed.
type Reaped struct {
	Lane int
	Cell string // a matrix cell or job ("" for the lane)
	PID  int    // the process that died (0 if none was recorded)
}

func (r Reaped) String() string {
	who := fmt.Sprintf("swim %d", r.Lane)
	if r.Cell != "" {
		who += " (" + r.Cell + ")"
	}
	pid := ""
	if r.PID > 0 {
		pid = fmt.Sprintf(" (pid %d)", r.PID)
	}
	return who + ": its process died without ending its round" + pid + "; recorded it as interrupted (swim log " + fmt.Sprint(r.Lane) + ")"
}

// ReapDead closes the rounds of lanes whose process died without ending
// them (SIGKILL, a crash, a closed terminal, a reboot): status.yml says
// running, but no live process holds the lane (its pid file names a
// process that's gone, or one with another start time). Each such round
// ends as a killed one does (closeKilledRound): the step in flight is
// recovered as cut off, and _finish records END INTERRUPTED (exit 137),
// the chain entry and the final state. A lane whose process can't be told
// dead is left alone, as is one another machine runs (status.yml and its
// claim name the host). self is the swim binary.
//
// swim run and swim watch call it as they start, so a dead lane never
// stays running until someone runs swim status --rebuild.
func ReapDead(root string, cfg *config.Config, self string) []Reaped {
	if lane.CheckMode() {
		return nil
	}
	if st, _ := status.Load(root); st == nil || !anyRunning(st) {
		return nil // the common case: nothing to lock for
	}
	unlock, ok := reapLock(root)
	if !ok {
		return nil
	}
	defer unlock()
	st, _ := status.Load(root)
	if st == nil {
		return nil
	}
	var out []Reaped
	for _, l := range st.Lanes {
		if l.State != status.Running {
			continue
		}
		if !proc.Local(l.Host) {
			// Another machine sharing the repo runs it: this one can't see
			// its process, so it can't tell it died. That machine's swim
			// reaps it.
			continue
		}
		if _, alive := lane.Running(root, l.Lane); alive {
			continue
		}
		r, err := logparse.ParseFile(lane.Log(root, l.Lane))
		if err != nil {
			continue
		}
		if r.Found && !r.Finished {
			finishDead(root, self, l.Lane)
		} else {
			// No round in the log, or it ended and only status.yml missed it.
			status.Update(root, l.Lane, cfg.Lanes, func(s *status.Lane) {
				if s.State != status.Running {
					return
				}
				s.PID, s.Host = 0, ""
				switch {
				case !r.Found || !r.Finished:
					s.State, s.Reason = status.Interrupted, "its process died"
				case r.ExitCode == 0:
					s.State = status.Passed
				case r.ExitCode == 129 || r.ExitCode == 130 || r.ExitCode == 137 || r.ExitCode == 143 || r.Interrupt:
					s.State = status.Interrupted
				default:
					s.State = status.Failed
				}
				if r.Finished {
					s.ExitCode = status.Int(r.ExitCode)
				}
			})
		}
		out = append(out, Reaped{Lane: l.Lane, PID: l.PID})
	}
	return out
}

// reapLock is held while a dead lane's round is ended, here or by
// closeKilledRound: two swim runs (or a run and a watch) that both see the
// lane dead never end its round twice.
func reapLock(root string) (unlock func(), ok bool) {
	if err := os.MkdirAll(status.Dir(root), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(filepath.Join(status.Dir(root), "reap.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return nil, false
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}

func anyRunning(st *status.File) bool {
	for _, l := range st.Lanes {
		if l.State == status.Running {
			return true
		}
	}
	return false
}

// finishDead ends a dead lane's round with swim _finish, as
// the launcher does for a lane it saw killed. It runs as an outside
// command, not as any lane's process, and its summary isn't shown.
func finishDead(root, self string, n int) {
	cmd := exec.Command(self, "_finish", fmt.Sprint(n), "--exit", "137")
	cmd.Dir = root
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "SWIM_LAUNCHED", approval.EnvVar, approval.TokenEnv, "SWIM_RUN", lane.CheckEnv:
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "SWIM_COLOR=0")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Run()
}
