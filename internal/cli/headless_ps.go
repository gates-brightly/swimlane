package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/version"
)

// Runners: the headless workers (swim watch --headless) on this machine,
// in any directory. Each one registers its directory when it starts
// (~/.config/swim/runners/<id>.yml), so swim ps finds it wherever it
// runs (one an older swim started shows once it restarts). The
// directory's .swim/headless.lock is what says a worker runs there: the
// registry only says where to look.

// runnerEntry is one registered directory: where, and the worker that
// last started there.
type runnerEntry struct {
	Root    string `yaml:"root"`
	PID     int    `yaml:"pid"`
	Mode    string `yaml:"mode"`
	Started string `yaml:"started"`
	Version string `yaml:"version,omitempty"`
}

// headlessRunner is a headless worker running now.
type headlessRunner struct {
	Root       string
	PID        int
	Mode       string    // foreground, detached, systemd, launchd; "" if unknown
	Started    time.Time // zero if unknown
	Registered bool
}

func runnersDir() string { return filepath.Join(filepath.Dir(config.Path()), "runners") }

// registerRunner records that this process is root's headless worker.
// Best effort: a worker runs whether or not swim ps can list it.
func registerRunner(root, mode string) {
	dir := runnersDir()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	data, _ := yaml.Marshal(runnerEntry{Root: root, PID: os.Getpid(), Mode: mode, Started: time.Now().UTC().Format(time.RFC3339), Version: version.String()})
	tmp := filepath.Join(dir, "."+headlessID(root)+".tmp")
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, filepath.Join(dir, headlessID(root)+".yml"))
	}
}

// runnerEntries are the registered directories; a directory that's gone
// (or has no .swim any more) is dropped from the registry.
func runnerEntries() []runnerEntry {
	files, _ := filepath.Glob(filepath.Join(runnersDir(), "*.yml"))
	var out []runnerEntry
	for _, f := range files {
		data, err := os.ReadFile(f)
		var e runnerEntry
		if err != nil || yaml.Unmarshal(data, &e) != nil || e.Root == "" {
			continue
		}
		if fi, err := os.Stat(filepath.Join(e.Root, ".swim")); err != nil || !fi.IsDir() {
			os.Remove(f)
			continue
		}
		out = append(out, e)
	}
	return out
}

// runningRunners are the headless workers running now, by directory.
func runningRunners() []headlessRunner {
	seen := map[string]bool{}
	var out []headlessRunner
	for _, e := range runnerEntries() {
		pid, held := headlessHolder(e.Root)
		if !held || seen[e.Root] {
			continue
		}
		seen[e.Root] = true
		r := headlessRunner{Root: e.Root, PID: pid, Registered: true}
		if e.PID == pid {
			r.Mode = e.Mode
			r.Started, _ = time.Parse(time.RFC3339, e.Started)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Root < out[b].Root })
	return out
}

// runnerMode is how root's worker runs: its mode, or the service a unit
// installed for it names.
func runnerMode(r headlessRunner) string {
	if r.Mode != "" {
		return r.Mode
	}
	if u := installedUnits(r.Root); len(u) > 0 {
		return u[0][0] + "?"
	}
	return "?"
}

// cmdPs is swim ps: the headless workers running on this machine.
func cmdPs(args []string) error {
	var raw bool
	rest, err := flags{bools: map[string]*bool{"yaml": &raw}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usagef("usage: swim ps [--yaml]")
	}
	rs := runningRunners()
	type row struct {
		Dir       string `yaml:"dir"`
		PID       int    `yaml:"pid"`
		Mode      string `yaml:"mode"`
		Started   string `yaml:"started,omitempty"`
		Running   []int  `yaml:"running"`
		Queued    int    `yaml:"queued"`
		Hub       string `yaml:"hub,omitempty"`
		Name      string `yaml:"name,omitempty"`
		Namespace string `yaml:"namespace,omitempty"`
	}
	rows := []row{}
	for _, r := range rs {
		w := row{Dir: r.Root, PID: r.PID, Mode: runnerMode(r), Running: []int{}}
		if !r.Started.IsZero() {
			w.Started = r.Started.UTC().Format(time.RFC3339)
		}
		if st, _ := status.Load(r.Root); st != nil {
			for _, l := range st.Lanes {
				if l.State == status.Running {
					w.Running = append(w.Running, l.Lane)
				}
			}
		}
		w.Queued = queuedCount(r.Root)
		if h, err := loadHub(r.Root); err == nil {
			w.Name, w.Namespace = h.Name, hubNamespace(r.Root, h)
			if u, err := url.Parse(h.URL); err == nil {
				w.Hub = u.Host
			}
		}
		rows = append(rows, w)
	}
	if raw {
		out, _ := yaml.Marshal(map[string]any{"schema": "swim.ps/v1", "runners": rows})
		fmt.Print(string(out))
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("swim ps: no headless swimmers running (swim watch --headless --detach starts one in a repo)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PID\tMODE\tUP\tLANES\tHUB\tDIR")
	for _, w := range rows {
		up := "?"
		if t, err := time.Parse(time.RFC3339, w.Started); err == nil {
			up = upFor(time.Since(t))
		}
		lanes := "idle"
		if len(w.Running) > 0 {
			lanes = "running " + strings.Trim(strings.ReplaceAll(fmt.Sprint(w.Running), " ", ","), "[]")
		}
		if w.Queued > 0 {
			lanes += fmt.Sprintf(", %d queued", w.Queued)
		}
		hubCol := "—"
		if w.Hub != "" {
			hubCol = w.Hub
			if w.Name != "" {
				hubCol = w.Name + "@" + w.Hub
			}
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", w.PID, w.Mode, up, lanes, hubCol, tildePath(w.Dir))
	}
	tw.Flush()
	return nil
}

// queuedCount is how many batches wait in root's queue.
func queuedCount(root string) int {
	files, _ := filepath.Glob(filepath.Join(root, ".swim", "queue", "*.queue"))
	return len(files)
}

// upFor writes an uptime: 45s, 12m, 3h 12m, 2d 4h.
func upFor(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
	return fmt.Sprintf("%dd %dh", s/86400, s%86400/3600)
}

// runnerStop is swim stop outside a lane: this directory's headless
// worker, or with --all every one on this machine. Each stops after the
// batch in hand, as swim watch --headless --stop does.
func runnerStop(args []string) error {
	var all bool
	rest, err := flags{bools: map[string]*bool{"all": &all}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usagef("usage: swim stop [--all]   (in a lane script: swim stop \"<reason>\")")
	}
	if !all {
		root, cfg, err := repo()
		if err != nil {
			return err
		}
		return headlessStop(root, cfg)
	}
	rs := runningRunners()
	if len(rs) == 0 {
		fmt.Println("swim stop: no headless swimmers running")
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, len(rs))
	for i, r := range rs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = stopRunner(r)
		}()
	}
	wg.Wait()
	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "swim stop: %s: %v\n", tildePath(rs[i].Root), err)
		}
	}
	if failed > 0 {
		return exitError{1}
	}
	return nil
}

// stopRunner sends r SIGTERM, once sure it's still that directory's
// worker, and waits (up to its term_grace plus 30s) for it to let go.
func stopRunner(r headlessRunner) error {
	where := tildePath(r.Root)
	pid, held := headlessHolder(r.Root)
	if !held {
		fmt.Printf("%s: already stopped\n", where)
		return nil
	}
	if ok, cmdline := headlessProcess(pid); !ok {
		return fmt.Errorf("pid %d holds .swim/headless.lock but isn't swim watch --headless (%q); not signalling it", pid, cmdline)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal pid %d: %w", pid, err)
	}
	term := 30 * time.Second
	if cfg, err := config.Load(r.Root); err == nil {
		_, term = cfg.Graces()
	}
	wait := term + 30*time.Second
	fmt.Printf("%s: sent SIGTERM to pid %d; it stops after the batch in hand (waiting up to %s)\n", where, pid, wait)
	for end := time.Now().Add(wait); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, held := headlessHolder(r.Root); !held {
			msg := "stopped"
			if u := installedUnits(r.Root); len(u) > 0 {
				msg += fmt.Sprintf(" (a %s service: it starts again at login; swim watch --headless --uninstall removes it)", u[0][0])
			}
			fmt.Printf("%s: %s\n", where, msg)
			return nil
		}
	}
	return fmt.Errorf("pid %d still holds .swim/headless.lock after %s; swim stop again forces its lanes", pid, wait)
}
