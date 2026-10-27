package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/doctor"
	"github.com/gates-brightly/swimlane/internal/hub"
)

// swim watch --headless: the queue worker with no terminal, in the
// foreground under a supervisor, detached (--detach), or installed as a
// systemd or launchd service (--install). It never shows the watch screen
// and never reads stdin. One per repo: .swim/headless.lock (flock) holds
// its pid, and a second one exits 2 naming it. It needs a hub enrollment:
// with no terminal, the hub is the only place it can be seen.

const (
	headlessLogMax  = 10 << 20 // .swim/headless.log rotates past this
	headlessLogKeep = 3        // and keeps headless.log.1 .. .3
	// headlessModeEnv tells the worker how it was started: "detached" (by
	// --detach) or the service manager a unit names. Read once and unset,
	// so lanes never see it.
	headlessModeEnv = "SWIM_HEADLESS_MODE"
	// headlessLogEnv names the log a detached worker writes to, so it can
	// rotate and reopen it.
	headlessLogEnv = "SWIM_HEADLESS_LOG"
)

// headlessInfo is what hubSnapshot tells the hub about this worker; nil
// unless it runs headless.
var headlessInfo *hub.HeadlessInfo

// watchYes is set by swim watch -y, for hubSnapshot.
var watchYes bool

// headlessFlags are swim watch's headless-only flags.
type headlessFlags struct {
	detach, status, stop, uninstall, iMeanIt bool
	install                                  string
}

// any reports whether one of them was given (they all need --headless).
func (h headlessFlags) any() bool {
	return h.detach || h.status || h.stop || h.uninstall || h.iMeanIt || h.install != ""
}

func headlessPath(root, name string) string { return filepath.Join(root, ".swim", "headless."+name) }

// headlessNoHub refuses a headless worker in a directory not enrolled with
// a hub.
func headlessNoHub(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("--headless needs a hub: this machine isn't logged in to one (swim auth login URL --access read|readwrite). With no terminal, the hub is the only place the swimmer can be seen")
	}
	return err
}

// headlessLock takes .swim/headless.lock and writes this process's pid in
// it. With another worker holding it, release is nil and holder is that
// worker's pid.
func headlessLock(root string) (release func(), holder int, err error) {
	path := headlessPath(root, "lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, 0, err
	}
	// A few tries: --status and --stop probe the lock for an instant.
	for try := 0; ; try++ {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil || !errors.Is(err, syscall.EWOULDBLOCK) || try == 3 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, _ := headlessHolder(root)
			return nil, pid, nil
		}
		return nil, 0, err
	}
	f.Truncate(0)
	f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, 0, nil
}

// headlessHolder is the pid in .swim/headless.lock, and whether a process
// holds the lock now (the pid is stale otherwise).
func headlessHolder(root string) (pid int, held bool) {
	f, err := os.Open(headlessPath(root, "lock"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	pid = readPid(f)
	if syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return pid, false
	}
	if pid == 0 { // it just took the lock and hasn't written its pid yet
		time.Sleep(50 * time.Millisecond)
		f.Seek(0, 0)
		pid = readPid(f)
	}
	return pid, true
}

func readPid(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	return pid
}

// pidFile is the pid in .swim/headless.pid (0 for none).
func pidFile(root string) int {
	f, err := os.Open(headlessPath(root, "pid"))
	if err != nil {
		return 0
	}
	defer f.Close()
	return readPid(f)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	// A killed worker nobody reaped yet is a zombie: gone all the same.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := strings.LastIndexByte(string(data), ')'); i >= 0 && strings.HasPrefix(string(data[i+1:]), " Z") {
			return false
		}
	} else if out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output(); err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
		return false
	}
	return true
}

// headlessProcess reports whether pid runs swim watch --headless, with its
// command line (for the refusal when it doesn't).
func headlessProcess(pid int) (bool, string) {
	var args []string
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		args = strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	} else if out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		args = strings.Fields(string(out))
	}
	watch, headless := false, false
	for i, a := range args {
		watch = watch || (i > 0 && a == "watch")
		headless = headless || a == "--headless"
	}
	return watch && headless, strings.Join(args, " ")
}

// headlessStart makes this process the repo's headless worker: it takes
// the lock (exit 2 if another worker holds it), writes the pidfile, points
// stdin at /dev/null and handles SIGHUP (reopen the log) from here on. A
// detached worker also rotates its log. release undoes it all on the way
// out.
func headlessStart(root string, yes, approved bool) (release func(), err error) {
	unlock, holder, err := headlessLock(root)
	if err != nil {
		return nil, err
	}
	if unlock == nil {
		fmt.Fprintf(os.Stderr, "swim watch: a headless swimmer already runs in this repo: pid %d holds .swim/headless.lock (swim watch --headless --status)\n", holder)
		return nil, exitError{2}
	}
	// Never read stdin: nothing here waits on a person.
	if null, err := os.Open(os.DevNull); err == nil {
		unix.Dup2(int(null.Fd()), 0)
		null.Close()
	}
	mode := os.Getenv(headlessModeEnv)
	if mode == "" {
		mode = "foreground"
	}
	var lw *headlessLog
	if path := os.Getenv(headlessLogEnv); path != "" {
		lw = &headlessLog{path: path, max: headlessLogMax, keep: headlessLogKeep, fds: []int{1, 2}}
	}
	os.Unsetenv(headlessModeEnv)
	os.Unsetenv(headlessLogEnv)
	// The pidfile comes last: --detach returns once it names this process,
	// and by then swim ps must list it.
	registerRunner(root, mode)
	pidPath := headlessPath(root, "pid")
	os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	headlessInfo = &hub.HeadlessInfo{Mode: mode, Yes: yes, Approved: approved, Since: time.Now().UTC().Format(time.RFC3339)}

	hups := make(chan os.Signal, 1)
	signal.Notify(hups, syscall.SIGHUP)
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-hups:
				switch {
				case lw == nil:
					fmt.Println("swim watch: SIGHUP: no log of its own to reopen (its output goes where it was started)")
				case lw.reopen() != nil:
					fmt.Fprintln(os.Stderr, "swim watch: SIGHUP: can't reopen "+lw.path)
				default:
					fmt.Println("swim watch: SIGHUP: reopened " + rel(root, lw.path))
				}
			case <-t.C:
				if lw != nil {
					if err := lw.rotateIfBig(); err != nil {
						fmt.Fprintln(os.Stderr, "swim watch: rotate "+lw.path+": "+err.Error())
					}
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		signal.Stop(hups)
		os.Remove(pidPath)
		unlock()
	}, nil
}

// headlessLog is a detached worker's own output file, written through fds
// (stdout and stderr). It rotates past max (keeping keep old ones) and
// reopens on SIGHUP, after logrotate moved it.
type headlessLog struct {
	path string
	max  int64
	keep int
	fds  []int
}

// reopen points fds at the file at path again (creating it).
func (l *headlessLog) reopen() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, fd := range l.fds {
		if err := unix.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}

// shift moves path to path.1, path.1 to path.2, ..., dropping the oldest.
func (l *headlessLog) shift() {
	for i := l.keep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
	}
	os.Rename(l.path, l.path+".1")
}

// rotateIfBig rotates once what fds write to is past max.
func (l *headlessLog) rotateIfBig() error {
	var st unix.Stat_t
	if err := unix.Fstat(l.fds[0], &st); err != nil || st.Size < l.max {
		return err
	}
	l.shift()
	return l.reopen()
}

// headlessDetach starts the headless worker in the background: swim
// re-executes itself (Go can't fork) in a new session, writing to
// .swim/headless.log, and waits up to 5s for it to take the lock.
func headlessDetach(root string, args []string) error {
	if pid, held := headlessHolder(root); held {
		fmt.Fprintf(os.Stderr, "swim watch: a headless swimmer already runs in this repo: pid %d holds .swim/headless.lock (swim watch --headless --status)\n", pid)
		return exitError{2}
	}
	lw := &headlessLog{path: headlessPath(root, "log"), max: headlessLogMax, keep: headlessLogKeep}
	if fi, err := os.Stat(lw.path); err == nil && fi.Size() >= lw.max {
		lw.shift()
	}
	f, err := os.OpenFile(lw.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n--- swim watch --headless --detach, %s ---\n", time.Now().Format(time.RFC3339))
	c := exec.Command(self(), append([]string{"watch"}, args...)...)
	c.Dir = root
	c.Env = append(os.Environ(), headlessModeEnv+"=detached", headlessLogEnv+"="+lw.path)
	c.Stdout, c.Stderr = f, f // stdin: /dev/null
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	pid := c.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-exited:
			fmt.Fprintf(os.Stderr, "swim watch: the headless swimmer exited at once (%v); the end of %s:\n", err, rel(root, lw.path))
			for _, l := range tailLines(lw.path, 10) {
				fmt.Fprintln(os.Stderr, "  "+l)
			}
			code := 1
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() > 0 {
				code = ee.ExitCode()
			}
			return exitError{code}
		case <-deadline:
			return fmt.Errorf("the headless swimmer (pid %d) didn't take .swim/headless.lock within 5s; see %s", pid, rel(root, lw.path))
		case <-tick.C:
			if pidFile(root) == pid {
				fmt.Printf("swim watch: headless swimmer started, pid %d (in .swim/headless.pid); its output goes to %s\n", pid, rel(root, lw.path))
				fmt.Println("  swim watch --headless --status    is it running?")
				fmt.Println("  swim watch --headless --stop      stop it after the batch in hand")
				return nil
			}
		}
	}
}

// headlessStatus says whether this repo's headless worker runs, and which
// unit is installed for it: exit 0 when it runs, 3 when it doesn't.
func headlessStatus(root string) error {
	pid, held := headlessHolder(root)
	running := false
	switch {
	case held && processAlive(pid):
		running = true
		fmt.Printf("swim watch --headless: running, pid %d (holds .swim/headless.lock)\n", pid)
	case held:
		running = true
		fmt.Printf("swim watch --headless: running: .swim/headless.lock is held, but pid %d isn't visible here\n", pid)
	default:
		pp := pidFile(root)
		switch {
		case pp == 0:
			fmt.Println("swim watch --headless: not running")
		case processAlive(pp):
			fmt.Printf("swim watch --headless: not running; stale .swim/headless.pid: pid %d is another process now. The lock is free\n", pp)
		default:
			fmt.Printf("swim watch --headless: not running; stale .swim/headless.pid: pid %d is gone (killed?). The lock is free\n", pp)
		}
	}
	if _, err := os.Stat(headlessPath(root, "log")); err == nil {
		fmt.Println("  log: .swim/headless.log")
	}
	units := installedUnits(root)
	for _, u := range units {
		fmt.Printf("  service: %s unit %s\n", u[0], u[1])
	}
	if len(units) == 0 {
		fmt.Println("  service: none installed (swim watch --headless --install systemd|launchd)")
	}
	if !running {
		return exitError{3}
	}
	return nil
}

// headlessStop sends SIGTERM to the worker holding the lock, once sure it
// is swim watch --headless, and waits for it to let go (up to term_grace
// plus 30s: the batch in hand stops at a step boundary, forced after
// term_grace).
func headlessStop(root string, cfg *config.Config) error {
	pid, held := headlessHolder(root)
	if !held {
		if pp := pidFile(root); pp != 0 {
			os.Remove(headlessPath(root, "pid"))
			fmt.Printf("swim watch: no headless swimmer running (removed a stale .swim/headless.pid naming pid %d)\n", pp)
			return nil
		}
		fmt.Println("swim watch: no headless swimmer running")
		return nil
	}
	if ok, cmdline := headlessProcess(pid); !ok {
		return fmt.Errorf("pid %d holds .swim/headless.lock but isn't swim watch --headless (%q); not signalling it", pid, cmdline)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	_, term := cfg.Graces()
	wait := term + 30*time.Second
	fmt.Printf("swim watch: sent SIGTERM to the headless swimmer (pid %d): it stops after the batch in hand (its lanes stop at the next step boundary, forced after term_grace %s); waiting up to %s\n", pid, term, wait)
	for end := time.Now().Add(wait); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, held := headlessHolder(root); !held {
			fmt.Println("swim watch: stopped")
			return nil
		}
	}
	return fmt.Errorf("pid %d still holds .swim/headless.lock after %s; swim watch --headless --stop again forces its lanes", pid, wait)
}

// headlessID names this repo's unit: its directory's name and a short hash
// of its absolute path, stable for as long as the repo stays where it is
// (swim-<id>.service, com.swim.<id>).
func headlessID(root string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(root)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 24 {
		name = strings.TrimRight(name[:24], "-")
	}
	if name == "" {
		name = "repo"
	}
	sum := sha256.Sum256([]byte(root))
	return name + "-" + hex.EncodeToString(sum[:4])
}

// systemdUnitPath is ~/.config/systemd/user/swim-<id>.service (under
// XDG_CONFIG_HOME when set, where systemd --user looks too).
func systemdUnitPath(id string) (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", "swim-"+id+".service"), nil
}

// launchdPaths are ~/Library/LaunchAgents/com.swim.<id>.plist and the log
// it writes, ~/Library/Logs/swim/<id>.log.
func launchdPaths(id string) (plist, log string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", "com.swim."+id+".plist"),
		filepath.Join(home, "Library", "Logs", "swim", id+".log"), nil
}

// installedUnits are the units written for this repo: [kind, path].
func installedUnits(root string) [][2]string {
	id := headlessID(root)
	var units [][2]string
	if p, err := systemdUnitPath(id); err == nil {
		if _, err := os.Stat(p); err == nil {
			units = append(units, [2]string{"systemd", p})
		}
	}
	if p, _, err := launchdPaths(id); err == nil {
		if _, err := os.Stat(p); err == nil {
			units = append(units, [2]string{"launchd", p})
		}
	}
	return units
}

// headlessUnit is what a unit file says.
type headlessUnit struct {
	ID, Label, Root string
	Args            []string // the swim binary's absolute path, then its arguments
	Path, User      string   // PATH and SWIM_USER, captured at install
	Log             string   // launchd: where its output goes
	StopTimeout     int      // seconds: term_grace + 30
	Yes             bool
}

// systemdTmpl is a user unit. KillMode=mixed sends SIGTERM to swim alone
// (its lanes run in their own process groups and are stopped by swim);
// only what's left at TimeoutStopSec is killed.
const systemdTmpl = `# swim headless worker for {{.Root}}
# Written by swim watch --headless --install systemd. Start it now and at
# every login: systemctl --user enable --now swim-{{.ID}}.service
# Remove it: swim watch --headless --uninstall, in the repo.
{{- if .Yes}}
# -y: this worker approves every guard in every batch (--i-mean-it).
{{- end}}

[Unit]
Description=swim headless worker ({{sdpath .Root}})
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
WorkingDirectory={{sdpath .Root}}
ExecStart={{sdexec .Args}}
Environment={{sdenv "PATH" .Path}}
Environment={{sdenv "SWIM_USER" .User}}
Environment=SWIM_HEADLESS_MODE=systemd
Restart=on-failure
RestartSec=10
# SIGTERM reaches swim alone: the batch in hand stops at a step boundary,
# forced after term_grace. What is left after term_grace + 30s is killed.
KillMode=mixed
TimeoutStopSec={{.StopTimeout}}

[Install]
WantedBy=default.target
`

// launchdTmpl is a LaunchAgent. KeepAlive restarts it when it fails, not
// when it was stopped (it exits 0 then); ExitTimeOut is term_grace + 30s.
const launchdTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xml .Label}}</string>
	<key>ProgramArguments</key>
	<array>
{{- range .Args}}
		<string>{{xml .}}</string>
{{- end}}
	</array>
	<key>WorkingDirectory</key>
	<string>{{xml .Root}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>{{xml .Path}}</string>
		<key>SWIM_USER</key>
		<string>{{xml .User}}</string>
		<key>SWIM_HEADLESS_MODE</key>
		<string>launchd</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ExitTimeOut</key>
	<integer>{{.StopTimeout}}</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>{{xml .Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{xml .Log}}</string>
</dict>
</plist>
`

var unitFuncs = template.FuncMap{
	// sdpath escapes a value for systemd (% starts a specifier).
	"sdpath": func(s string) string { return strings.ReplaceAll(s, "%", "%%") },
	"sdexec": func(args []string) string {
		q := make([]string, len(args))
		for i, a := range args {
			q[i] = strings.ReplaceAll(sdQuote(a), "$", "$$")
		}
		return strings.Join(q, " ")
	},
	"sdenv": func(k, v string) string { return sdQuote(k + "=" + v) },
	"xml": func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	},
}

// sdQuote double-quotes a word for a unit file.
func sdQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s)
	return `"` + s + `"`
}

func renderUnit(kind string, u headlessUnit) (string, error) {
	text := systemdTmpl
	if kind == "launchd" {
		text = launchdTmpl
	}
	t, err := template.New(kind).Funcs(unitFuncs).Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, u); err != nil {
		return "", err
	}
	return b.String(), nil
}

// headlessInstall writes a systemd user unit or a launchd LaunchAgent that
// runs swim watch --headless in this repo, and prints how to enable it; it
// never runs that itself. Standing -y needs --i-mean-it.
func headlessInstall(root string, cfg *config.Config, kind string, yes, iMeanIt, approved bool, interval string) error {
	if kind != "systemd" && kind != "launchd" {
		return usagef("--install takes systemd or launchd, got %q", kind)
	}
	if yes && !iMeanIt {
		return usagef("--install with -y writes a standing approval of every guard in every batch into a service that runs until removed; add --i-mean-it if that is what you want")
	}
	if strings.ContainsAny(root, "\n\r") {
		return fmt.Errorf("the repo's path has a line break; a unit file can't name it")
	}
	id := headlessID(root)
	args := []string{self(), "watch", "--headless"}
	if interval != "" {
		args = append(args, "--interval", interval)
	}
	if approved {
		args = append(args, "--approved")
	}
	if yes {
		args = append(args, "-y")
	}
	_, term := cfg.Graces()
	u := headlessUnit{ID: id, Label: "com.swim." + id, Root: root, Args: args, Path: os.Getenv("PATH"), User: operator(),
		StopTimeout: int((term + 30*time.Second).Seconds()), Yes: yes}
	var path string
	var err error
	if kind == "systemd" {
		path, err = systemdUnitPath(id)
	} else {
		path, u.Log, err = launchdPaths(id)
	}
	if err != nil {
		return err
	}
	text, err := renderUnit(kind, u)
	if err != nil {
		return err
	}
	dirs := []string{filepath.Dir(path)}
	if u.Log != "" {
		dirs = append(dirs, filepath.Dir(u.Log))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	verb := "wrote"
	if _, err := os.Stat(path); err == nil {
		verb = "updated"
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Printf("swim watch: %s %s\n", verb, path)
	fmt.Println("  it runs: " + strings.Join(args, " "))
	if yes {
		fmt.Println("  -y: it approves every guard in every batch (the hub shows this on its page)")
	}
	fmt.Println("swim doesn't start it; to start it now and at every login, run:")
	if kind == "systemd" {
		fmt.Println("  systemctl --user daemon-reload")
		fmt.Printf("  systemctl --user enable --now swim-%s.service\n", id)
		fmt.Println("to keep it running while you're logged out (and start it at boot):")
		who := os.Getenv("USER")
		if who == "" {
			who = "$USER"
		}
		fmt.Printf("  loginctl enable-linger %s\n", who)
		fmt.Printf("its output: journalctl --user -u swim-%s.service\n", id)
	} else {
		fmt.Printf("  launchctl bootstrap gui/%d %s\n", os.Getuid(), path)
		fmt.Printf("its output: %s\n", u.Log)
	}
	return nil
}

// headlessUninstall stops this repo's headless worker if it runs and
// removes its unit files, then says how to make the service manager
// forget them.
func headlessUninstall(root string, cfg *config.Config) error {
	if _, held := headlessHolder(root); held {
		if err := headlessStop(root, cfg); err != nil {
			return err
		}
	}
	id := headlessID(root)
	units := installedUnits(root)
	for _, u := range units {
		if err := os.Remove(u[1]); err != nil {
			return err
		}
		fmt.Println("swim watch: removed " + u[1])
		if u[0] == "systemd" {
			// enable's link; dangling now
			link := filepath.Join(filepath.Dir(u[1]), "default.target.wants", filepath.Base(u[1]))
			if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				os.Remove(link)
			}
			fmt.Println("  then run: systemctl --user daemon-reload")
		} else {
			fmt.Printf("  then run, if it's still loaded: launchctl bootout gui/%d/com.swim.%s\n", os.Getuid(), id)
		}
	}
	if len(units) == 0 {
		fmt.Printf("swim watch: no unit installed for this repo (swim-%s)\n", id)
	}
	return nil
}

// doctorHeadless is this repo's headless worker for swim doctor.
func doctorHeadless(root string) *doctor.Headless {
	d := &doctor.Headless{Units: installedUnits(root)}
	d.PID, d.Running = headlessHolder(root)
	if pp := pidFile(root); pp != 0 && !(d.Running && pp == d.PID) && !processAlive(pp) {
		d.Stale = pp
	}
	if h, err := loadHub(root); err == nil {
		d.HubCheck = func() error {
			c := hubClient(h)
			hc := *c.HTTP
			hc.Timeout = 5 * time.Second
			resp, err := hc.Get(strings.TrimRight(h.URL, "/") + "/api/health")
			if err != nil {
				return hub.Explain(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s answered %s", h.URL, resp.Status)
			}
			return nil
		}
	}
	return d
}
