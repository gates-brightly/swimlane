package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdHub runs the watch hub, or enrolls this directory with one:
//
//	swim hub [--addr HOST:PORT] [--token T]       serve the hub
//	swim hub enroll URL [--token T] [--name N]    report this directory's swim watch to it
//	swim hub leave                                stop reporting
//	swim hub status                               this directory's enrollment
func cmdHub(args []string) error {
	sub := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "serve":
		return hubServe(args)
	case "enroll":
		return hubEnroll(args)
	case "leave":
		return hubLeave(args)
	case "status":
		return hubStatus(args)
	}
	return usagef("usage: swim hub [serve|enroll|leave|status] (swim hub --help)")
}

func hubServe(args []string) error {
	var addr, token string
	rest, err := flags{strs: map[string]*string{"addr": &addr, "token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim hub [--addr HOST:PORT] [--token T]")
	}
	if addr == "" {
		addr = "127.0.0.1:7700"
	}
	if token == "" {
		token = os.Getenv("SWIM_HUB_TOKEN")
	}
	generated := false
	if token == "" {
		b := make([]byte, 16)
		rand.Read(b)
		token, generated = hex.EncodeToString(b), true
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	fmt.Printf("swim hub · %s\n", url)
	fmt.Printf("enroll a worker (in its repo):  swim hub enroll %s --token %s\n", url, token)
	if generated {
		fmt.Println("  (a new token each start: pass --token or SWIM_HUB_TOKEN to keep workers enrolled across restarts)")
	}
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); !net.ParseIP(host).IsLoopback() {
		fmt.Println("warning: the dashboard is read-only but unauthenticated: anyone who can reach " + url + " sees every worker's lanes, queue and log tails")
	}
	srv := &http.Server{Handler: hub.New(token).Handler(), ReadHeaderTimeout: 5 * time.Second}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		<-sigs
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// hubFile is this directory's enrollment (.swim/hub.yml, private).
type hubFile struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
}

func hubPath(root string) string { return filepath.Join(root, ".swim", "hub.yml") }

func loadHub(root string) (*hubFile, error) {
	data, err := os.ReadFile(hubPath(root))
	if err != nil {
		return nil, err
	}
	var h hubFile
	if err := yaml.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("%s: %w", hubPath(root), err)
	}
	return &h, nil
}

func hubWorker(root, name string) hub.Worker {
	host, _ := os.Hostname()
	if name == "" {
		name = filepath.Base(root)
	}
	return hub.Worker{Name: name, Host: host, Root: root, Version: version.String(), PID: os.Getpid()}
}

func hubEnroll(args []string) error {
	var token, name string
	rest, err := flags{strs: map[string]*string{"token": &token, "name": &name}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim hub enroll URL [--token T] [--name N]")
	}
	if token == "" {
		token = os.Getenv("SWIM_HUB_TOKEN")
	}
	if token == "" {
		return usagef("swim hub enroll needs the hub's token: --token T (swim hub prints it)")
	}
	root, _, err := repoNoMigrate()
	if err != nil {
		return err
	}
	w := hubWorker(root, name)
	c := hub.NewClient(rest[0], token, "")
	id, err := c.Enroll(w)
	if err != nil {
		return err
	}
	data, _ := yaml.Marshal(hubFile{URL: c.URL, Token: token, ID: id, Name: w.Name})
	os.MkdirAll(filepath.Dir(hubPath(root)), 0o755)
	if err := os.WriteFile(hubPath(root), data, 0o600); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: "hub", Detail: "enrolled with " + c.URL + " as " + w.Name})
	// Show this directory on the hub now, not only once swim watch starts.
	if cfg, err := loadCfg(root); err == nil {
		c.Report(hubSnapshot(root, cfg, w.Name, "enrolled; not watching (start swim watch)", pollEvery))
	}
	fmt.Printf("enrolled %s with %s as %s (id %s)\nswim watch here now reports to it: %s/w/%s\n", root, c.URL, w.Name, id, c.URL, id)
	return nil
}

func hubLeave(args []string) error {
	root, _, err := repoNoMigrate()
	if err != nil {
		return err
	}
	h, err := loadHub(root)
	if err != nil {
		return fmt.Errorf("this directory isn't enrolled with a hub")
	}
	if err := hub.NewClient(h.URL, h.Token, h.ID).Leave(); err != nil {
		fmt.Fprintln(os.Stderr, "swim hub: "+err.Error()+" (un-enrolled here anyway)")
	}
	os.Remove(hubPath(root))
	history.Log(root, history.Entry{Event: "hub", Detail: "left " + h.URL})
	fmt.Println("left " + h.URL)
	return nil
}

func hubStatus(args []string) error {
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	h, err := loadHub(root)
	if err != nil {
		fmt.Println("not enrolled with a hub (swim hub enroll URL --token T)")
		return nil
	}
	fmt.Printf("enrolled with %s as %s (id %s)\n", h.URL, h.Name, h.ID)
	c := hub.NewClient(h.URL, h.Token, h.ID)
	if err := c.Report(hubSnapshot(root, cfg, h.Name, "checked by swim hub status", pollEvery)); err != nil {
		return fmt.Errorf("hub unreachable: %w", err)
	}
	fmt.Printf("reachable: %s/w/%s\n", h.URL, h.ID)
	return nil
}

// hubReporter reports this directory to its hub every interval while
// swim watch runs (also during a batch, from its own goroutine). It does
// nothing when the directory isn't enrolled. setActivity says what the
// watch is doing; stop sends a last report and ends it.
func hubReporter(root string, every time.Duration) (setActivity func(string), stop func()) {
	h, err := loadHub(root)
	if err != nil {
		return func(string) {}, func() {}
	}
	c := hub.NewClient(h.URL, h.Token, h.ID)
	var mu sync.Mutex
	activity := "starting"
	done := make(chan struct{})
	finished := make(chan struct{})
	send := func() {
		mu.Lock()
		act := activity
		mu.Unlock()
		cfg, err := loadCfg(root)
		if err != nil {
			return
		}
		if err := c.Report(hubSnapshot(root, cfg, h.Name, act, every)); err != nil {
			reportHubError(err)
		} else {
			reportHubError(nil)
		}
	}
	go func() {
		defer close(finished)
		t := time.NewTicker(every)
		defer t.Stop()
		send()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				send()
			}
		}
	}()
	fmt.Printf("swim watch: reporting to %s/w/%s\n", h.URL, h.ID)
	return func(a string) {
			mu.Lock()
			activity = a
			mu.Unlock()
		}, func() {
			close(done)
			<-finished
			mu.Lock()
			activity = "stopped"
			mu.Unlock()
			send()
		}
}

var (
	hubErrMu   sync.Mutex
	hubErrLast string
)

// reportHubError says when the hub becomes unreachable, and when it's back,
// once each (never once per report).
func reportHubError(err error) {
	hubErrMu.Lock()
	defer hubErrMu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg == hubErrLast {
		return
	}
	switch {
	case msg != "":
		fmt.Fprintln(os.Stderr, "swim watch: hub unreachable: "+msg+" (still watching; will retry)")
	case hubErrLast != "":
		fmt.Fprintln(os.Stderr, "swim watch: hub reachable again")
	}
	hubErrLast = msg
}

// hubSnapshot is what this directory looks like now: lanes (with the end
// of each running lane's log, its running step included), the queue, and
// the end of .swim.log. Logs are already masked of secrets.
func hubSnapshot(root string, cfg *config.Config, name, activity string, every time.Duration) hub.Snapshot {
	s := hub.Snapshot{Worker: hubWorker(root, name), Activity: activity, At: time.Now().UTC().Format(time.RFC3339),
		Interval: every.Seconds(), Lanes: []hub.Lane{}, Queue: []hub.Batch{}, Events: []string{}}
	if st, _ := status.Load(root); st != nil {
		st.Normalize(cfg.Lanes)
		for _, l := range st.Lanes {
			hl := hub.Lane{Lane: l.Lane, State: l.State, Round: l.Round, Job: l.Job, Run: l.Run, Step: l.CurrentStep, Reason: l.Reason,
				Message: l.Message, Progress: l.Progress, MsgAt: l.MessageAt,
				Pass: l.Pass, Fail: l.Fail, Skip: l.Skip, Drift: l.Drift, Exit: l.ExitCode}
			if l.Round == "" {
				hl.Round = l.Pending
			}
			if l.StartedAt != nil {
				hl.Started = *l.StartedAt
			}
			if l.FinishedAt != nil {
				hl.Finished = *l.FinishedAt
			}
			if l.State == status.Running {
				hl.Tail = laneTail(root, l.Lane, 15)
			}
			s.Lanes = append(s.Lanes, hl)
		}
	}
	if entries, err := queue.List(root, true); err == nil {
		var done []hub.Batch
		for _, e := range entries {
			b := hub.Batch{Seq: e.Seq, ID: e.ID, State: e.State, By: e.By, Note: e.Note, Lanes: []int{}}
			for _, l := range e.Lanes {
				b.Lanes = append(b.Lanes, l.Lane)
			}
			if e.Result != nil {
				b.Result = e.Result.Result
			}
			if e.State == queue.Done {
				done = append(done, b)
			} else {
				s.Queue = append(s.Queue, b)
			}
		}
		if len(done) > 5 {
			done = done[len(done)-5:]
		}
		s.Queue = append(done, s.Queue...)
	}
	s.Events = tailLines(history.Path(root), 15)
	return s
}

// laneTail is the end of lane n's current round, with the step running now.
func laneTail(root string, n, max int) []string {
	var lines []string
	if rounds, _ := splitRounds(lane.Log(root, n)); len(rounds) > 0 {
		lines = rounds[len(rounds)-1]
	}
	if sp, why := inFlight(root, n); sp != nil {
		lines = append(lines, sp.Block(why)...)
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

// tailLines is the last n non-comment lines of a file.
func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if l := sc.Text(); l != "" && !strings.HasPrefix(l, "#") {
			all = append(all, l)
		}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}
