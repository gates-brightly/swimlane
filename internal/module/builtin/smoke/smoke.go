// Package smoke is the built-in smoke module: a committed list of checks,
// each run once, judged by an HTTP status (and how long it took) or a TCP
// connection, never by matching what a response says. The file is pinned
// with the lane (# Includes:), so the approval covers the checks too.
//
//	# smoke/orders.yml
//	base: https://${ORDERS_HOST}      # ${VAR} from the lane's environment
//	timeout: 10s                      # each request (default 10s)
//	checks:
//	  - get: /health
//	  - name: list orders
//	    get: /api/orders?limit=1
//	    status: 200                   # a code, a list [200, 204] or a class 2xx (default 2xx)
//	    within: 2s                    # slower fails
//	    headers: {Authorization: "Bearer ${ORDERS_TOKEN}"}
//	  - post: /api/ping
//	    body: '{}'
//	    status: [200, 204]
//	  - tcp: db.internal:5432
//
// Retrying is the step's job: mod --retry 3 --backoff 10s smoke FILE.
package smoke

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod smoke FILE [--base URL] [--timeout 10s]"

// File is a smoke file.
type File struct {
	Base    string  `yaml:"base"`
	Timeout string  `yaml:"timeout"`
	Checks  []Check `yaml:"checks"`
}

// Check is one check: a request (get, post, put, delete, head) or tcp.
type Check struct {
	Name    string            `yaml:"name"`
	Get     string            `yaml:"get"`
	Post    string            `yaml:"post"`
	Put     string            `yaml:"put"`
	Delete  string            `yaml:"delete"`
	Head    string            `yaml:"head"`
	TCP     string            `yaml:"tcp"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
	Status  Status            `yaml:"status"`
	Within  string            `yaml:"within"`
}

// Status is what a check expects: codes, or classes (2xx); default 2xx.
type Status []string

func (s *Status) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = Status{n.Value}
		return nil
	case yaml.SequenceNode:
		*s = nil
		for _, c := range n.Content {
			*s = append(*s, c.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: status is a code, a class (2xx) or a list", n.Line)
}

func (s Status) match(code int) bool {
	if len(s) == 0 {
		return code/100 == 2
	}
	for _, want := range s {
		w := strings.ToLower(strings.TrimSpace(want))
		if len(w) == 3 && strings.HasSuffix(w, "xx") && int(w[0]-'0') == code/100 {
			return true
		}
		if n, err := strconv.Atoi(w); err == nil && n == code {
			return true
		}
	}
	return false
}

func (s Status) String() string {
	if len(s) == 0 {
		return "2xx"
	}
	return strings.Join(s, ",")
}

// request is the check's method and path ("" for a tcp check).
func (c Check) request() (string, string, error) {
	var method, path string
	n := 0
	for _, m := range []struct{ name, v string }{{"GET", c.Get}, {"POST", c.Post}, {"PUT", c.Put}, {"DELETE", c.Delete}, {"HEAD", c.Head}, {"TCP", c.TCP}} {
		if m.v != "" {
			method, path = m.name, m.v
			n++
		}
	}
	if n != 1 {
		return "", "", errors.New("a check is exactly one of get, post, put, delete, head or tcp")
	}
	return method, path, nil
}

// Run is mod smoke FILE ...
func Run(c *gomod.Ctx) error {
	a, err := gomod.ParseArgs(c.Args, []string{"base", "timeout"}, nil)
	if err != nil {
		return err
	}
	if len(a.Pos) != 1 || len(a.Rest) > 0 {
		return gomod.Usage{Msg: usage}
	}
	data, err := c.ReadPinned(a.Pos[0])
	if err != nil {
		return err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("%s: %w", a.Pos[0], err)
	}
	if len(f.Checks) == 0 {
		return fmt.Errorf("%s has no checks", a.Pos[0])
	}
	expand := func(s string) string {
		return os.Expand(s, func(k string) string { return c.Getenv(k) })
	}
	base := expand(a.Flag("base", f.Base))
	timeout := 10 * time.Second
	for _, t := range []string{f.Timeout, a.Flag("timeout", "")} {
		if t == "" {
			continue
		}
		if timeout, err = time.ParseDuration(t); err != nil || timeout <= 0 {
			return gomod.Usage{Msg: fmt.Sprintf("smoke: timeout %q isn't a duration like 10s", t)}
		}
	}
	client := &http.Client{Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	failed := 0
	for i, ck := range f.Checks {
		method, target, err := ck.request()
		if err != nil {
			return fmt.Errorf("%s: check %d: %w", a.Pos[0], i+1, err)
		}
		var within time.Duration
		if ck.Within != "" {
			if within, err = time.ParseDuration(ck.Within); err != nil || within <= 0 {
				return fmt.Errorf("%s: check %d: within %q isn't a duration", a.Pos[0], i+1, ck.Within)
			}
		}
		name := ck.Name
		if name == "" {
			name = strings.ToLower(method) + " " + target
		}
		start := time.Now()
		ok, got := false, ""
		if method == "TCP" {
			conn, err := net.DialTimeout("tcp", expand(target), timeout)
			if err == nil {
				conn.Close()
				ok, got = true, "connected"
			} else {
				got = err.Error()
			}
		} else {
			ok, got = httpCheck(client, method, join(base, expand(target)), expand(ck.Body), ck.Headers, expand, ck.Status)
		}
		took := time.Since(start)
		if ok && within > 0 && took > within {
			ok, got = false, fmt.Sprintf("%s, but took %s (within %s)", got, took.Round(time.Millisecond), within)
		}
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(c.Stdout, "smoke: %s %s: %s (%s)\n", mark, name, got, took.Round(time.Millisecond))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(f.Checks))
	}
	fmt.Fprintf(c.Stdout, "smoke: all %d checks passed\n", len(f.Checks))
	return nil
}

func join(base, path string) string {
	if strings.Contains(path, "://") || base == "" {
		return path
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func httpCheck(client *http.Client, method, url, body string, headers map[string]string, expand func(string) string, want Status) (bool, string) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return false, err.Error()
	}
	for k, v := range headers {
		req.Header.Set(k, expand(v))
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false, "no answer within " + client.Timeout.String()
		}
		return false, err.Error()
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if want.match(resp.StatusCode) {
		return true, "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return false, fmt.Sprintf("HTTP %d, want %s", resp.StatusCode, want)
}
