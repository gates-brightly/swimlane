package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
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
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdHub runs the watch hub, or enrolls this directory with one:
//
//	swim hub [--addr HOST:PORT] [--token T] [--tls-cert F --tls-key F]   serve the hub
//	swim hub enroll URL [--token T] [--name N] [--ca F]  report this directory's swim watch to it
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
	case "login":
		return hubLoginCmd(args)
	case "approve":
		return hubApproveCmd(args, false)
	case "unapprove":
		return hubApproveCmd(args, true)
	}
	return usagef("usage: swim hub [serve|enroll|leave|status|login|approve|unapprove] (swim hub --help)")
}

func hubServe(args []string) error {
	var addr, token, stateDir, adminToken, certFile, keyFile string
	var noTUI, insecure bool
	rest, err := flags{bools: map[string]*bool{"no-tui": &noTUI, "insecure": &insecure},
		strs: map[string]*string{"addr": &addr, "token": &token, "state": &stateDir, "admin-token": &adminToken, "tls-cert": &certFile, "tls-key": &keyFile}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim hub [--addr HOST:PORT] [--token T] [--state DIR] [--admin-token T] [--tls-cert FILE --tls-key FILE] [--insecure] [--no-tui]")
	}
	if (certFile == "") != (keyFile == "") {
		return usagef("--tls-cert and --tls-key go together")
	}
	if stateDir == "" {
		stateDir = filepath.Join(filepath.Dir(config.Path()), "hub")
	}
	if adminToken == "" {
		adminToken = os.Getenv("SWIM_HUB_ADMIN_TOKEN")
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
	useTLS := certFile != ""
	if err := listenPolicy(addr, useTLS, insecure); err != nil {
		return err
	}
	var certs *certReloader
	if useTLS {
		if certs, err = newCertReloader(certFile, keyFile); err != nil {
			return err
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	if useTLS {
		ln = tls.NewListener(ln, certs.tlsConfig())
		url = "https://" + ln.Addr().String()
	}
	var notes []display.PickRow
	note := func(color, text string) {
		notes = append(notes, display.PickRow{Text: text, Color: color})
	}
	enroll := "swim hub enroll " + url + " --token " + token
	if useTLS {
		enroll += " [--ca CERT.pem]"
	}
	note(ui.Dim, "enroll a worker (in its repo):  "+enroll)
	if generated {
		note(ui.Dim, "  (a new token each start: pass --token or SWIM_HUB_TOKEN to keep workers enrolled across restarts)")
	}
	if useTLS {
		note(ui.Dim, "  (--ca: the hub's certificate or its CA, when workers don't already trust it; "+certFile+" reloads when it changes)")
	}
	if host, _, _ := net.SplitHostPort(ln.Addr().String()); !hub.LoopbackHost(host) {
		if !useTLS {
			note(ui.Yellow, "warning: plain HTTP on the network (--insecure): tokens, logs and secrets cross it unencrypted; serve TLS with --tls-cert/--tls-key")
		}
		note(ui.Yellow, "warning: the dashboard is read-only but unauthenticated: anyone who can reach "+url+" sees every worker's lanes, queue and log tails")
	}
	h, err := hub.NewWithState(token, adminToken, stateDir)
	if err != nil {
		return err
	}
	note(ui.Dim, "state: "+stateDir+" · admin token: "+hub.AdminTokenPath(stateDir)+" (swim hub login "+url+" --admin-token \"$(cat "+hub.AdminTokenPath(stateDir)+")\")")
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	stopped := make(chan struct{})
	shutdown := sync.OnceFunc(func() {
		close(stopped)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})
	go func() {
		select {
		case <-sigs:
			shutdown()
		case <-stopped:
		}
	}()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	if !noTUI && ui.IsTTY(os.Stdin) && ui.IsTTY(os.Stdout) && display.TUIAllowed(os.Getenv) {
		hubScreen(h, url, notes, stopped)
		shutdown()
	} else {
		fmt.Printf("swim hub · %s\n", url)
		for _, n := range notes {
			fmt.Println(n.Text)
		}
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	fmt.Println("swim hub stopped")
	return nil
}

// hubScreen is the hub's live screen: every worker (Enter opens one, Esc
// comes back) until Ctrl-C, or until stopped closes (a signal).
func hubScreen(h *hub.Server, url string, notes []display.PickRow, stopped <-chan struct{}) {
	color := ui.ColorEnabled(os.Stdout)
	list := &display.Picker{In: os.Stdin, Out: os.Stdout, Color: color,
		Title: func() string { return "swim hub · " + url + " · " + hub.Summary(h.Workers()) }}
	detail := &display.Picker{In: os.Stdin, Out: os.Stdout, Color: color}
	until := func() bool {
		select {
		case <-stopped:
			return true
		default:
			return false
		}
	}
	for !until() {
		res, err := list.Pick("swim hub · "+url, "↑ ↓ pick  enter open  a approve  u unapprove  ctrl-c quit",
			string(display.PickEnter)+"au", func() []display.PickRow { return hub.ListRows(h.Workers(), list.Sel, notes, time.Now()) }, time.Second, until)
		if err != nil || res.Quit || res.ID == "" {
			return
		}
		id := strings.TrimPrefix(res.ID, "w:")
		if res.Key == 'a' || res.Key == 'u' {
			msg, err := h.Approve(id)
			if res.Key == 'u' {
				msg, err = h.Unapprove(id)
			}
			list.Note = msg
			if err != nil {
				list.Note = err.Error()
			}
			continue
		}
		detail.Sel = ""
		for !until() {
			var v *hub.View
			for _, w := range h.Workers() {
				if w.ID == id {
					w := w
					v = &w
				}
			}
			if v == nil {
				list.Note = "that worker left"
				break
			}
			res, err := detail.Pick("swim hub · "+v.Name, "↑ ↓ lane  esc back  ctrl-c quit", string(display.PickEsc), func() []display.PickRow {
				for _, w := range h.Workers() {
					if w.ID == id {
						return hub.WorkerRows(w, detail.Sel, time.Now())
					}
				}
				return []display.PickRow{{Text: "this worker left the hub (esc)"}}
			}, time.Second, until)
			if err != nil || res.Quit {
				return
			}
			if res.Key == display.PickEsc {
				break
			}
		}
	}
}

// hubFile is this directory's enrollment (.swim/hub.yml, private).
type hubFile struct {
	URL         string `yaml:"url"`
	Token       string `yaml:"token"`                  // the hub's enroll token
	WorkerToken string `yaml:"worker_token,omitempty"` // this worker's own, from enrolling
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	CA          string `yaml:"ca,omitempty"` // PEM the hub's https certificate must chain to (enroll --ca)
}

func saveHub(root string, h *hubFile) error {
	data, _ := yaml.Marshal(h)
	os.MkdirAll(filepath.Dir(hubPath(root)), 0o755)
	return os.WriteFile(hubPath(root), data, 0o600)
}

// hubClient is this directory's connection to its hub.
func hubClient(h *hubFile) *hub.Client {
	c, err := hub.NewClientTLS(h.URL, h.Token, h.ID, []byte(h.CA))
	if err != nil {
		// A CA that no longer parses: fail closed (no system roots) with
		// the reason on every call rather than trust something else.
		c = hub.NewClient(h.URL, h.Token, h.ID)
		c.HTTP.Transport = brokenTransport{fmt.Errorf("%s: ca: %w", hubPath(""), err)}
	}
	c.WorkerToken = h.WorkerToken
	return c
}

// brokenTransport fails every request with err.
type brokenTransport struct{ err error }

func (b brokenTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, b.err }

// keepHubToken saves a new worker token the hub gave out (it enrolled us
// again after forgetting us).
func keepHubToken(root string, h *hubFile, c *hub.Client) {
	if c.WorkerToken != h.WorkerToken || c.ID != h.ID {
		h.WorkerToken, h.ID = c.WorkerToken, c.ID
		saveHub(root, h)
	}
}

// workerKeyPath is this directory's own age key for hub secrets (one per
// enrolled directory, private to the operator's config dir).
func workerKeyPath(root string) string {
	host, _ := os.Hostname()
	return filepath.Join(filepath.Dir(config.Path()), "worker-keys", hub.IDFor(host, root)+".key")
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
	w := hub.Worker{Name: name, Host: host, Root: root, Version: version.String(), PID: os.Getpid()}
	if id, err := secrets.WorkerKey(workerKeyPath(root)); err == nil {
		w.Key = id.Recipient().String()
	}
	return w
}

// hubCA reads --ca FILE for a hub URL: "" without one; an http URL with
// one is a mistake (there is no certificate to check).
func hubCA(url, file string, insecure bool) (string, error) {
	if err := hub.CheckURL(url, insecure); err != nil {
		return "", usageError{err.Error()}
	}
	if file == "" {
		return "", nil
	}
	if !strings.HasPrefix(strings.ToLower(url), "https://") {
		return "", usagef("--ca is for an https:// hub")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("--ca: %w", err)
	}
	return string(data), nil
}

func hubEnroll(args []string) error {
	var token, name, caFile string
	var insecure bool
	rest, err := flags{bools: map[string]*bool{"insecure": &insecure}, strs: map[string]*string{"token": &token, "name": &name, "ca": &caFile}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim hub enroll URL [--token T] [--name N] [--ca FILE] [--insecure]")
	}
	ca, err := hubCA(rest[0], caFile, insecure)
	if err != nil {
		return err
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
	c, err := hub.NewClientTLS(rest[0], token, "", []byte(ca))
	if err != nil {
		return fmt.Errorf("--ca %s: %w", caFile, err)
	}
	id, err := c.Enroll(w)
	if err != nil {
		return err
	}
	if err := saveHub(root, &hubFile{URL: c.URL, Token: token, WorkerToken: c.WorkerToken, ID: id, Name: w.Name, CA: ca}); err != nil {
		return err
	}
	history.Log(root, history.Entry{Event: "hub", Detail: "enrolled with " + c.URL + " as " + w.Name})
	// Show this directory on the hub now, not only once swim watch starts.
	if cfg, err := loadCfg(root); err == nil {
		c.Report(hubSnapshot(root, cfg, w.Name, "enrolled; not watching (start swim watch)", pollEvery))
	}
	fmt.Printf("enrolled %s with %s as %s (id %s)\nswim watch here now reports to it: %s/w/%s\n", root, c.URL, w.Name, id, c.URL, id)
	if w.Key != "" {
		fmt.Printf("its key for secrets: %s (approve it on the hub: a on its row, or swim hub approve %s)\n", hub.KeyFingerprint(w.Key), w.Name)
	}
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
	if err := hubClient(h).Leave(); err != nil {
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
	c := hubClient(h)
	if err := c.Report(hubSnapshot(root, cfg, h.Name, "checked by swim hub status", pollEvery)); err != nil {
		return fmt.Errorf("hub unreachable: %w", err)
	}
	keepHubToken(root, h, c)
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
	c := hubClient(h)
	hubErrMu.Lock()
	hubURL, hubReported, hubErrLast = h.URL, false, ""
	hubErrMu.Unlock()
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
			keepHubToken(root, h, c)
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
	hubErrMu    sync.Mutex
	hubErrLast  string
	hubURL      string    // the hub this watch reports to ("" when not enrolled)
	hubReported bool      // a report has finished, so hubErrLast says how it went
	hubSince    time.Time // when the hub last went online or offline
	hubQuiet    bool      // the watch screen shows the link: don't print over it
)

// reportHubError says when the hub becomes unreachable (offline mode), and
// when it's back online, once each (never once per report).
func reportHubError(err error) {
	hubErrMu.Lock()
	defer hubErrMu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	first := !hubReported
	hubReported = true
	if msg == hubErrLast && !first {
		return
	}
	hubSince = time.Now()
	if hubQuiet {
		hubErrLast = msg
		return
	}
	switch {
	case msg != "":
		fmt.Fprintln(os.Stderr, "swim watch: offline mode: hub unreachable: "+msg+" (still watching; will retry)")
	case hubErrLast != "":
		fmt.Fprintln(os.Stderr, "swim watch: hub online again")
	}
	hubErrLast = msg
}

// hubLink is the watch screen's line for the hub link: "" when not
// enrolled, else whether the hub is online, with its color.
func hubLink() (string, string) {
	hubErrMu.Lock()
	defer hubErrMu.Unlock()
	switch {
	case hubURL == "":
		return "", ""
	case !hubReported:
		return "hub: connecting to " + hubURL, ui.Dim
	case hubErrLast != "":
		return "(offline mode) hub " + hubURL + " unreachable since " + hubSince.Format("15:04:05") + "; retrying", ui.Yellow
	}
	return "(hub online) " + hubURL, ui.Green
}

// quietHub stops reportHubError printing: the watch screen shows hubLink.
func quietHub(on bool) {
	hubErrMu.Lock()
	hubQuiet = on
	hubErrMu.Unlock()
}

// hubSnapshot is what this directory looks like now: lanes (with the end
// of each running lane's log, its running step included), the queue, and
// the end of .swim.log. Logs are already masked of secrets.
func hubSnapshot(root string, cfg *config.Config, name, activity string, every time.Duration) hub.Snapshot {
	s := hub.Snapshot{Worker: hubWorker(root, name), Activity: activity, At: time.Now().UTC().Format(time.RFC3339),
		Interval: every.Seconds(), Lanes: []hub.Lane{}, Queue: []hub.Batch{}, Events: []string{}}
	if st, _ := status.Load(root); st != nil {
		inUse := map[int]bool{} // now: the worker outlives any one config load
		for _, n := range lane.Associated(root) {
			inUse[n] = true
		}
		st.Normalize(cfg.Lanes)
		for _, l := range st.Lanes {
			if !inUse[l.Lane] {
				continue // a lane no longer in use (archived, or never was)
			}
			hl := hub.Lane{Lane: l.Lane, State: l.State, Round: l.Round, Job: l.Job, Run: l.Run, Step: l.CurrentStep, Reason: l.Reason,
				Message: l.Message, Progress: l.Progress, MsgAt: l.MessageAt, Cells: cellsText(root, l),
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

// cellsText is a lane's cells summary for the hub, a lane of jobs' by job
// id (1.4).
func cellsText(root string, l status.Lane) string {
	if info, _ := lane.ReadScript(root, l.Lane); info.IsGroup() {
		return l.CellsTextAs(func(c string) string { return info.SubLabel(l.Lane, c) })
	}
	return l.CellsText()
}
