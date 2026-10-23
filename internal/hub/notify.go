package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Alerts: the hub POSTs a JSON alert to each webhook (swim hub --webhook)
// when something needs a person: a run failed, or passed with its change
// dry-run; a recurring schedule was missed, failed, skipped or expired; a
// worker stopped reporting; a guard waits for an answer. The body carries
// "text" (and "content") too, so a Slack, Mattermost or Discord incoming
// webhook shows it as a message. Each alert is sent once (a worker that
// goes stale again, or a schedule missed on another day, is a new one).

// Alert events.
const (
	AlertRunFailed        = "run_failed"
	AlertRunDryRun        = "run_dry_run"
	AlertScheduleMissed   = "schedule_missed"
	AlertScheduleFailed   = "schedule_failed"
	AlertScheduleSkipped  = "schedule_skipped"
	AlertScheduleExpired  = "schedule_expired"
	AlertScheduleExpiring = "schedule_expiring"
	AlertWorkerStale      = "worker_stale"
	AlertApprovalWaiting  = "approval_waiting"
)

// Alert is one webhook body.
type Alert struct {
	Event     string `json:"event"`
	Severity  string `json:"severity"` // error, warning or info
	Text      string `json:"text"`     // the message, for Slack and Mattermost
	Content   string `json:"content"`  // the same, for Discord
	Hub       string `json:"hub,omitempty"`
	Worker    string `json:"worker,omitempty"`
	Name      string `json:"name,omitempty"`
	Host      string `json:"host,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Lane      int    `json:"lane,omitempty"`
	Run       string `json:"run,omitempty"`
	Schedule  string `json:"schedule,omitempty"`
	URL       string `json:"url,omitempty"` // the hub page to look at
	At        string `json:"at"`
}

// AlertCheck is how often the hub looks for stale workers, missed
// schedules and waiting guards.
const AlertCheck = 30 * time.Second

type notifier struct {
	urls   []string
	client *http.Client
	queue  chan Alert
	mu     sync.Mutex
	sent   map[string]time.Time // alert keys, so each is sent once
	retry  time.Duration
	stop   chan struct{}
}

// Webhooks sends alerts to urls (http or https), and starts watching for
// them; none turns alerts off.
func (s *Server) Webhooks(urls ...string) error {
	for _, u := range urls {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return fmt.Errorf("webhook %q: an http(s) URL", redactURL(u))
		}
	}
	if len(urls) == 0 {
		return nil
	}
	n := &notifier{urls: urls, client: &http.Client{Timeout: 10 * time.Second}, queue: make(chan Alert, 256),
		sent: map[string]time.Time{}, retry: 5 * time.Second, stop: make(chan struct{})}
	s.notify = n
	go n.send()
	go func() {
		t := time.NewTicker(AlertCheck)
		defer t.Stop()
		for {
			select {
			case <-n.stop:
				return
			case <-t.C:
				s.CheckAlerts()
			}
		}
	}()
	return nil
}

// PublicURL is where people reach the hub, for the links in alerts.
func (s *Server) PublicURL(u string) { s.publicURL = strings.TrimRight(u, "/") }

// redactURL is u without its path and query (a Slack webhook's secret).
func redactURL(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Scheme + "://" + p.Host + "/…"
	}
	return "…"
}

func (n *notifier) send() {
	for {
		select {
		case <-n.stop:
			return
		case a := <-n.queue:
			body, _ := json.Marshal(a)
			for _, u := range n.urls {
				for try := 0; try < 3; try++ {
					resp, err := n.client.Post(u, "application/json", bytes.NewReader(body))
					if err == nil {
						resp.Body.Close()
						if resp.StatusCode < 300 {
							break
						}
					}
					if try < 2 {
						time.Sleep(n.retry << try)
					}
				}
			}
		}
	}
}

// alert sends a once per key (nothing without webhooks).
func (s *Server) alert(key string, a Alert) {
	n := s.notify
	if n == nil {
		return
	}
	now := s.now()
	n.mu.Lock()
	if _, done := n.sent[key]; done {
		n.mu.Unlock()
		return
	}
	n.sent[key] = now
	for k, t := range n.sent {
		if now.Sub(t) > 14*24*time.Hour {
			delete(n.sent, k)
		}
	}
	n.mu.Unlock()
	a.At, a.Hub, a.Content = now.UTC().Format(time.RFC3339), s.publicURL, a.Text
	if a.URL != "" {
		a.URL = s.publicURL + a.URL
		a.Text += " " + a.URL
		a.Content = a.Text
	}
	select {
	case n.queue <- a:
	default: // a webhook that can't keep up loses alerts, not the hub
	}
}

func workerAlert(v View, event, severity, text, page string) Alert {
	return Alert{Event: event, Severity: severity, Text: "swim: " + v.Name + " (" + v.Host + "): " + text, Worker: v.ID, Name: v.Name, Host: v.Host, Namespace: v.Namespace, URL: page}
}

// CheckAlerts looks for what needs a person now: stale workers, missed,
// failed, skipped and expiring schedules, guards waiting for an answer.
func (s *Server) CheckAlerts() {
	if s.notify == nil {
		return
	}
	for _, v := range s.Workers() {
		e := "/e/" + url.PathEscape(v.ID)
		if v.Stale {
			s.alert("stale/"+v.ID+"/"+v.Seen.UTC().Format(time.RFC3339), workerAlert(v, AlertWorkerStale, "warning",
				"stopped reporting (last report "+v.Seen.UTC().Format(time.RFC3339)+"): is its swim watch running?", e+"/runner"))
		}
		for _, p := range v.Waiting {
			s.alert("approval/"+v.ID+"/"+p.ID, workerAlert(v, AlertApprovalWaiting, "info",
				fmt.Sprintf("lane %d waits for an answer to %s (%s)", p.Lane, p.Flag, p.Desc), e+"/questions/"+url.PathEscape(p.ID)))
		}
		for _, sc := range v.Schedules {
			ev, sev := "", "error"
			switch sc.Status {
			case ScheduleMissed:
				ev = AlertScheduleMissed
			case ScheduleFailed:
				ev = AlertScheduleFailed
			case ScheduleSkipped:
				ev = AlertScheduleSkipped
			case ScheduleExpired:
				ev = AlertScheduleExpired
			case ScheduleExpiring:
				ev, sev = AlertScheduleExpiring, "warning"
			default:
				continue
			}
			// The same verdict about the same occurrence is one alert.
			occ := sc.Next
			if sc.Status != ScheduleMissed && sc.Last != nil {
				occ = sc.Last.Occurrence
			}
			if sc.Status == ScheduleSkipped && len(sc.Skipped) > 0 {
				occ = sc.Skipped[len(sc.Skipped)-1]
			}
			lanes := strings.Trim(fmt.Sprint(sc.Lanes), "[]")
			a := workerAlert(v, ev, sev, fmt.Sprintf("schedule %s (lanes %s, %s) %s: %s", sc.ID, lanes, sc.Cadence, sc.Status, sc.Why), "/schedules")
			a.Schedule = sc.ID
			if len(sc.Lanes) > 0 {
				a.Lane = sc.Lanes[0]
			}
			s.alert("schedule/"+v.ID+"/"+sc.ID+"/"+sc.Status+"/"+occ, a)
		}
	}
}

// runAlert alerts on a run that just arrived: failed, or passed with a
// change that only dry-ran.
func (s *Server) runAlert(id string, rep *RunReport) {
	if s.notify == nil {
		return
	}
	v, ok := s.worker(id)
	if !ok {
		return
	}
	var bad, dry []string
	lane := 0
	for _, l := range rep.Run.Lanes {
		if l.State == "failed" || l.State == "interrupted" {
			bad = append(bad, fmt.Sprintf("lane %d %s (%s)", l.Lane, l.State, l.Round))
			if lane == 0 {
				lane = l.Lane
			}
		}
	}
	for i := range rep.Jobs {
		if d := DryRuns(&rep.Jobs[i]); len(d) > 0 {
			dry = append(dry, fmt.Sprintf("lane %d: %s", rep.Jobs[i].Lane, strings.Join(d, "; ")))
		}
	}
	page := "/e/" + url.PathEscape(id) + "/runs/" + url.PathEscape(rep.Run.Run)
	switch {
	case len(bad) > 0:
		a := workerAlert(v, AlertRunFailed, "error", "run "+rep.Run.Run+": "+strings.Join(bad, ", "), page)
		a.Run, a.Lane = rep.Run.Run, lane
		s.alert("run/"+id+"/"+rep.Run.Run, a)
	case len(dry) > 0:
		a := workerAlert(v, AlertRunDryRun, "warning", "run "+rep.Run.Run+" passed, but its change didn't run: "+strings.Join(dry, "; "), page)
		a.Run = rep.Run.Run
		s.alert("run/"+id+"/"+rep.Run.Run, a)
	}
}

// CloseAlerts stops the alert watcher and sender.
func (s *Server) CloseAlerts() {
	if s.notify != nil {
		close(s.notify.stop)
	}
}
