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
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/cache"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/queue"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/ui"
	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdHub serves the hub; joining one, and the admin's side, are swim auth:
//
//	swim hub [serve] [--addr HOST:PORT] [--state DIR] [--tls-cert F --tls-key F] [--public-url URL] [--private] [--retain AGE] [--retain-bytes SIZE]
func cmdHub(args []string) error {
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return usagef("usage: swim hub [serve] [flags]   (joining a hub and its admin's side: swim auth --help)")
	}
	return hubServe(args)
}

func hubServe(args []string) error {
	var addr, token, stateDir, adminToken, certFile, keyFile, publicURL, retain, retainBytes string
	var noTUI, insecure, private bool
	rest, err := flags{bools: map[string]*bool{"no-tui": &noTUI, "insecure": &insecure, "private": &private},
		strs: map[string]*string{"addr": &addr, "token": &token, "state": &stateDir, "admin-token": &adminToken, "tls-cert": &certFile, "tls-key": &keyFile,
			"public-url": &publicURL, "retain": &retain, "retain-bytes": &retainBytes}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim hub [--addr HOST:PORT] [--token T] [--state DIR] [--admin-token T] [--tls-cert FILE --tls-key FILE] [--public-url URL] [--private] [--retain AGE] [--retain-bytes SIZE] [--insecure] [--no-tui]")
	}
	if (certFile == "") != (keyFile == "") {
		return usagef("--tls-cert and --tls-key go together")
	}
	if publicURL != "" {
		if publicURL, err = checkPublicURL(publicURL); err != nil {
			return err
		}
	}
	keepAge, keepBytes, err := runRetention(retain, retainBytes)
	if err != nil {
		return err
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
	listening := url
	if publicURL != "" {
		url = publicURL
	}
	var notes []display.PickRow
	note := func(color, text string) {
		notes = append(notes, display.PickRow{Text: text, Color: color})
	}
	enroll := "swim auth login " + url + " --access read|readwrite"
	if useTLS {
		enroll += " [--ca CERT.pem]"
	}
	note(ui.Dim, "join a swimmer (once per machine):  "+enroll+"  (no token needed: it waits until an admin admits it, on the Admin page or with swim auth admit NAME)")
	_ = generated // the enroll token only matters to workers from before admission
	if useTLS {
		note(ui.Dim, "  (--ca: the hub's certificate or its CA, when swimmers don't already trust it; "+certFile+" reloads when it changes)")
	}
	if publicURL != "" {
		note(ui.Dim, "listening on "+listening+", reached at "+publicURL+" (through a proxy on this host)")
	}
	host, _, _ := net.SplitHostPort(ln.Addr().String())
	if !hub.LoopbackHost(host) && !useTLS {
		note(ui.Yellow, "warning: plain HTTP on the network (--insecure): tokens, logs and secrets cross it unencrypted; serve TLS with --tls-cert/--tls-key")
	}
	if !private && (!hub.LoopbackHost(host) || publicURL != "") {
		note(ui.Yellow, "warning: viewing the dashboard needs no login: anyone who can reach "+url+" sees every swimmer's lanes, queue and log tails (--private makes it need a hub user: swim auth users add)")
	}
	h, err := hub.NewWithState(token, adminToken, stateDir)
	if err != nil {
		return err
	}
	h.SecureCookies(useTLS || strings.HasPrefix(publicURL, "https://"))
	h.BehindProxy(publicURL != "")
	h.Private(private)
	h.RetainRuns(keepAge, keepBytes)
	note(ui.Dim, "state: "+stateDir+" · admin token: "+hub.AdminTokenPath(stateDir)+" (swim auth login "+url+" --admin-token \"$(cat "+hub.AdminTokenPath(stateDir)+")\")")
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 5 * time.Second}
	// Open event streams would hold Shutdown to its deadline (quitting
	// the live screen waits on it).
	srv.RegisterOnShutdown(h.CloseEvents)
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
				list.Note = "that swimmer left"
				break
			}
			res, err := detail.Pick("swim hub · "+v.Name, "↑ ↓ lane  esc back  ctrl-c quit", string(display.PickEsc), func() []display.PickRow {
				for _, w := range h.Workers() {
					if w.ID == id {
						return hub.WorkerRows(w, detail.Sel, time.Now())
					}
				}
				return []display.PickRow{{Text: "this swimmer left the hub (esc)"}}
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
	// Namespace is the hub tab this directory shows under (enroll
	// --namespace, else its origin remote's; hubNamespace).
	Namespace string `yaml:"namespace,omitempty"`
	// Access is what this enrollment lets the hub do (read or readwrite;
	// swim auth login --access, read live for a directory that joined
	// through it); "" from before access levels.
	Access string `yaml:"access,omitempty"`
	CA     string `yaml:"ca,omitempty"` // PEM the hub's https certificate must chain to (enroll --ca)
	// Via is "auth" for an enrollment made through swim auth: it lasts
	// while this machine stays logged in to that hub, and enrolls again
	// with the machine's token.
	Via string `yaml:"via,omitempty"`
	// What a headless worker has carried out of the hub's commands, so a
	// restarted (or replaying) hub can't make it act twice: the hub's
	// epoch, the last seq seen in it, and the ids of recent commands.
	Epoch      string   `yaml:"epoch,omitempty"`
	LastSeq    int      `yaml:"last_seq,omitempty"`
	RecentCIDs []string `yaml:"recent_cids,omitempty"`
}

// hubFileMu serialises this process's read-modify-writes of .swim/hub.yml
// (the reporter keeps the token; the command loop the replay state).
var hubFileMu sync.Mutex

func saveHub(root string, h *hubFile) error {
	data, _ := yaml.Marshal(h)
	os.MkdirAll(filepath.Dir(hubPath(root)), 0o755)
	// Whole or not at all: a reader never sees half a file.
	tmp := fmt.Sprintf("%s.%d.tmp", hubPath(root), os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, hubPath(root))
}

// hubClient is this directory's connection to its hub.
func hubClient(h *hubFile) *hub.Client {
	token := h.Token
	if h.Via == viaAuth {
		if a, err := loadAuth(); err == nil {
			token = a.Token // the machine's: enrolls it again if the hub forgot it
		}
	}
	c, err := hub.NewClientTLS(h.URL, token, h.ID, []byte(h.CA))
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
		hubFileMu.Lock()
		defer hubFileMu.Unlock()
		// Only the token and id: the command loop keeps the rest current.
		if cur, err := loadHub(root); err == nil {
			cur.WorkerToken, cur.ID = h.WorkerToken, h.ID
			saveHub(root, cur)
		}
	}
}

func hubPath(root string) string { return filepath.Join(root, ".swim", "hub.yml") }

// loadHub is root's enrollment. One made through swim auth counts only
// while this machine is still logged in to that hub.
func loadHub(root string) (*hubFile, error) {
	h, err := readHub(root)
	if err != nil {
		return nil, err
	}
	if h.Via == viaAuth {
		a, err := loadAuth()
		if err != nil || strings.TrimRight(a.URL, "/") != strings.TrimRight(h.URL, "/") {
			return nil, fmt.Errorf("%s: joined through swim auth with %s, and this machine is no longer logged in to it: %w", hubPath(root), h.URL, os.ErrNotExist)
		}
		// Access is the machine's, as swim auth login last set it.
		h.Access = a.Access
	}
	return h, nil
}

// readHub is root's .swim/hub.yml as it is.
func readHub(root string) (*hubFile, error) {
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

func hubWorker(root, name, ns, access string) hub.Worker {
	host, _ := os.Hostname()
	if name == "" {
		name = filepath.Base(root)
	}
	w := hub.Worker{Name: name, Host: host, Root: root, Namespace: ns, Access: access, Version: version.String(), PID: os.Getpid()}
	return w
}

// hubNamespace is the namespace root reports under: hub.yml's (enroll
// --namespace, or what enroll worked out), else its origin remote's, for
// a directory enrolled before namespaces.
func hubNamespace(root string, h *hubFile) string {
	if h != nil && h.Namespace != "" {
		return h.Namespace
	}
	return hub.NamespaceFor(cache.Origin(root), root)
}

// enrollNamespace is the namespace a new enrollment reports under: its
// repo's (hubNamespace), or for a directory outside git, the machine it
// runs on as HOST:IP (the address it reaches the hub from).
func enrollNamespace(root, hubURL string) string {
	if inGitRepo(root) {
		return hubNamespace(root, nil)
	}
	host, _ := os.Hostname()
	if host = strings.Split(host, ".")[0]; host == "" {
		host = "unknown"
	}
	if ip := localIP(hubURL); ip != "" {
		return hub.CleanNamespace(host + ":" + ip)
	}
	return hub.CleanNamespace(host)
}

// localIP is the address this machine reaches hubURL's host from ("" if
// it can't tell): a UDP dial sends nothing, it only picks the route.
func localIP(hubURL string) string {
	u, err := neturl.Parse(hubURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	c, err := net.DialTimeout("udp", net.JoinHostPort(u.Hostname(), port), 2*time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsUnspecified() {
		return a.IP.String()
	}
	return ""
}

// hubAccessOf is what this directory's enrollment lets its hub do ("" when
// it isn't enrolled, or enrolled before access levels).
func hubAccessOf(root string) string {
	if h, err := loadHub(root); err == nil {
		return h.Access
	}
	return ""
}

// askAccess asks at a terminal what the hub may do here; elsewhere it's
// read, said so.
func askAccess() (string, error) {
	if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
		fmt.Println("access: read (the hub shows this directory and fetches its logs; --access readwrite lets it send commands too)")
		return config.AccessRead, nil
	}
	fmt.Print("What may the hub do here?\n  read       show this directory and fetch its logs\n  readwrite  also send commands: run, queue, interrupt, answer, ...\naccess [read]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch a := strings.ToLower(strings.TrimSpace(line)); a {
	case "", "r", config.AccessRead:
		return config.AccessRead, nil
	case "rw", "w", "write", config.AccessReadWrite:
		return config.AccessReadWrite, nil
	default:
		return "", usagef("access %q: read or readwrite", a)
	}
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

// hubReporter reports this directory to its hub every interval while
// swim watch runs (also during a batch, from its own goroutine). It does
// nothing when the directory isn't enrolled. setActivity says what the
// watch is doing; stop sends a last report and ends it.
func hubReporter(root string, every time.Duration) (setActivity func(string), stop func()) {
	h, err := ensureHub(root)
	if err != nil {
		return func(string) {}, func() {}
	}
	c := hubClient(h)
	ns := hubNamespace(root, h)
	hubErrMu.Lock()
	hubURL, hubReported, hubErrLast = h.URL, false, ""
	hubErrMu.Unlock()
	var mu sync.Mutex
	activity := "starting"
	done := make(chan struct{})
	finished := make(chan struct{})
	streamer := newHubStreamer()
	send := func() {
		mu.Lock()
		act := activity
		mu.Unlock()
		cfg, err := loadCfg(root)
		if err != nil {
			return
		}
		if err := c.Report(hubSnapshot(root, cfg, h.Name, ns, act, every)); err != nil {
			reportHubError(err)
		} else {
			reportHubError(nil)
			keepHubToken(root, h, c)
			// Streamed logs (hub.logs: stream), then finished runs; what
			// the hub didn't take goes again next report.
			streamer.send(root, cfg, c)
			sendHubRuns(root, cfg, h, c)
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
	fmt.Printf("swim watch: reporting to %s/e/%s/lanes\n", h.URL, h.ID)
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
func hubSnapshot(root string, cfg *config.Config, name, ns, activity string, every time.Duration) hub.Snapshot {
	access := hubAccessOf(root)
	s := hub.Snapshot{Worker: hubWorker(root, name, ns, access), Activity: activity, At: time.Now().UTC().Format(time.RFC3339),
		Interval: every.Seconds(), Lanes: []hub.Lane{}, Queue: []hub.Batch{}, Events: []string{}}
	if headlessInfo != nil {
		h := *headlessInfo
		h.Allow, h.LogsOff = cfg.HubAllow(access), !cfg.HeadlessLogs()
		s.Headless = &h
		s.Waiting = waitingApprovals()
	} else if watchYes {
		s.AutoApprove = "swim watch -y"
	}
	s.Config = hubConfigDoc(cfg, access)
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
				Message: l.Message, Progress: l.Progress, MsgAt: l.MessageAt,
				Pass: l.Pass, Fail: l.Fail, Skip: l.Skip, Drift: l.Drift, Exit: l.ExitCode,
				AutoApproved: l.AutoApproved, AutoVia: l.AutoVia, FlagApproved: l.FlagApproved, DryRun: l.DryRun}
			if info, err := lane.ReadScript(root, l.Lane); err == nil {
				hl.Task = info.Task
			}
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
			} else if s.Headless != nil && (cfg.HubAllows(access, config.HubRun) || cfg.HubAllows(access, config.HubQueue)) {
				// What a run or queue command from the hub would pin it to.
				hl.Script, hl.Manifest = runPin(root, cfg, l.Lane)
			}
			if l.PausedAt != "" && l.Alive() {
				hl.Paused = fmt.Sprintf("by %s after %q", or(l.PausedBy, "?"), l.PausedAfter)
			}
			s.Lanes = append(s.Lanes, hl)
		}
	}
	if entries, err := queue.List(root, true); err == nil {
		var done []hub.Batch
		for _, e := range entries {
			b := hub.Batch{Seq: e.Seq, ID: e.ID, State: e.State, By: e.By, Note: e.Note, Lanes: []int{}, Queued: e.Created}
			// A batch scheduled by an older swim still waits for its time.
			if e.NotBefore != "" {
				b.At = "not before " + e.NotBefore
				if e.State == queue.Queued && !e.Due(time.Now()) {
					b.State = "scheduled"
				}
			}
			for _, l := range e.Lanes {
				b.Lanes = append(b.Lanes, l.Lane)
			}
			if e.Result != nil {
				b.Result, b.Run, b.Started, b.Finished = e.Result.Result, e.Result.Run, e.Result.Started, e.Result.Finished
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
	s.Logs = cfg.HubLogs()
	s.Sanitize()
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

// hubConfigDoc is the configuration a worker shows its hub (read only):
// swim config's, then what its enrollment lets the hub do, masked of
// secrets.
func hubConfigDoc(cfg *config.Config, access string) string {
	var b strings.Builder
	b.WriteString(configDoc(cfg))
	shown := access
	if shown == "" {
		shown = "(none: enrolled before access levels; swim.yml's headless.allow)"
	}
	fmt.Fprintf(&b, "hub_access: %s   # swim auth login URL --access read|readwrite\n", shown)
	fmt.Fprintf(&b, "hub_commands: [%s]   # what a headless swimmer takes from the hub\n", strings.Join(cfg.HubAllow(access), ", "))
	return cfg.Masker(os.Environ()).String(b.String())
}

func inGitRepo(root string) bool {
	c := exec.Command("git", "rev-parse", "--show-toplevel")
	c.Dir = root
	out, err := c.Output()
	return err == nil && filepath.Clean(strings.TrimSpace(string(out))) == filepath.Clean(root)
}
