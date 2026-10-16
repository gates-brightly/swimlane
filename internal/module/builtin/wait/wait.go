// Package wait is the built-in wait module: it waits until a service is
// ready, judged by an HTTP status, a TCP connection or a command's exit
// code, never by matching text in output. It tries every --every until it
// passes or --for runs out (the step's own --timeout and the round's
// Timeout: still apply).
package wait

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod wait http URL [--status 2xx|200,204] | tcp HOST:PORT | cmd -- CMD [ARGS...] | kube-rollout RESOURCE [--namespace N] [--context C]   (each: [--for 10m] [--every 5s])"

// Run is mod wait http|tcp|cmd|kube-rollout ...
func Run(c *gomod.Ctx) error {
	a, err := gomod.ParseArgs(c.Args, []string{"for", "every", "status", "namespace", "context"}, nil)
	if err != nil {
		return err
	}
	if len(a.Pos) == 0 {
		return gomod.Usage{Msg: usage}
	}
	budget, err := duration(a, "for", 10*time.Minute)
	if err != nil {
		return err
	}
	every, err := duration(a, "every", 5*time.Second)
	if err != nil {
		return err
	}
	kind, pos := a.Pos[0], a.Pos[1:]
	var probe func() (bool, string)
	var what string
	switch {
	case kind == "http" && len(pos) == 1 && len(a.Rest) == 0:
		ok, err := statusMatcher(a.Flag("status", "2xx"))
		if err != nil {
			return err
		}
		what = pos[0] + " to answer " + a.Flag("status", "2xx")
		probe = httpProbe(pos[0], min(every, 30*time.Second)+5*time.Second, ok)
	case kind == "tcp" && len(pos) == 1 && len(a.Rest) == 0:
		if _, _, err := net.SplitHostPort(pos[0]); err != nil {
			return gomod.Usage{Msg: "wait tcp needs HOST:PORT"}
		}
		what = pos[0] + " to accept a connection"
		probe = tcpProbe(pos[0], min(every, 10*time.Second)+time.Second)
	case kind == "cmd" && len(pos) == 0 && len(a.Rest) > 0:
		what = strings.Join(a.Rest, " ") + " to exit 0"
		probe = cmdProbe(c, a.Rest)
	case kind == "kube-rollout" && len(pos) == 1 && len(a.Rest) == 0:
		return kubeRollout(c, pos[0], a, budget)
	default:
		return gomod.Usage{Msg: usage}
	}
	fmt.Fprintf(c.Stdout, "wait: for %s (up to %s, every %s)\n", what, budget, every)
	start := time.Now()
	deadline := start.Add(budget)
	last, lastShown := "", time.Time{}
	for n := 1; ; n++ {
		ok, state := probe()
		if ok {
			fmt.Fprintf(c.Stdout, "wait: ready after %s (attempt %d: %s)\n", since(start), n, state)
			return nil
		}
		// Say what changed, and every 30s that it hasn't.
		if state != last || time.Since(lastShown) >= 30*time.Second {
			fmt.Fprintf(c.Stdout, "wait: not yet (attempt %d, %s): %s\n", n, since(start), state)
			last, lastShown = state, time.Now()
		}
		if time.Now().Add(every).After(deadline) {
			return fmt.Errorf("not ready after %s (%d attempts; last: %s)", budget, n, state)
		}
		time.Sleep(every)
	}
}

func duration(a gomod.Args, name string, def time.Duration) (time.Duration, error) {
	v := a.Flag(name, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, gomod.Usage{Msg: fmt.Sprintf("--%s %q isn't a duration like 30s or 10m", name, v)}
	}
	return d, nil
}

func since(t time.Time) string { return time.Since(t).Round(time.Second).String() }

// statusMatcher reads --status: a class (2xx) or a list of codes (200,204).
func statusMatcher(spec string) (func(int) bool, error) {
	var classes []int
	codes := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if len(part) == 3 && strings.HasSuffix(part, "xx") && part[0] >= '1' && part[0] <= '5' {
			classes = append(classes, int(part[0]-'0'))
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 100 || n > 599 {
			return nil, gomod.Usage{Msg: fmt.Sprintf("--status %q: want codes (200,204) or a class (2xx)", spec)}
		}
		codes[n] = true
	}
	return func(code int) bool {
		for _, c := range classes {
			if code/100 == c {
				return true
			}
		}
		return codes[code]
	}, nil
}

func httpProbe(url string, timeout time.Duration, ok func(int) bool) func() (bool, string) {
	client := &http.Client{Timeout: timeout,
		// A redirect is an answer: judge its status, don't follow it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func() (bool, string) {
		resp, err := client.Get(url)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return false, "no answer within " + timeout.String()
			}
			return false, shortErr(err)
		}
		resp.Body.Close()
		return ok(resp.StatusCode), "HTTP " + strconv.Itoa(resp.StatusCode)
	}
}

func tcpProbe(addr string, timeout time.Duration) func() (bool, string) {
	return func() (bool, string) {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return false, shortErr(err)
		}
		conn.Close()
		return true, "connected"
	}
}

func cmdProbe(c *gomod.Ctx, argv []string) func() (bool, string) {
	return func() (bool, string) {
		cmd := c.Command(argv[0], argv[1:]...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return true, "exit 0"
		}
		var ee interface{ ExitCode() int }
		state := shortErr(err)
		if errors.As(err, &ee) {
			state = "exit " + strconv.Itoa(ee.ExitCode())
		}
		if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); len(lines) > 0 && lines[len(lines)-1] != "" {
			state += " (" + truncate(lines[len(lines)-1], 120) + ")"
		}
		return false, state
	}
}

// kubeRollout waits with kubectl rollout status, which follows the rollout
// itself and exits 0 once it's complete.
func kubeRollout(c *gomod.Ctx, res string, a gomod.Args, budget time.Duration) error {
	args := []string{"rollout", "status", res, "--timeout=" + budget.String()}
	if ns := a.Flag("namespace", ""); ns != "" {
		args = append(args, "--namespace", ns)
	}
	if ctx := a.Flag("context", ""); ctx != "" {
		args = append(args, "--context", ctx)
	}
	fmt.Fprintf(c.Stdout, "wait: for the rollout of %s (up to %s)\n", res, budget)
	cmd := c.Command("kubectl", args...)
	cmd.Stdout, cmd.Stderr = c.Stdout, c.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the rollout of %s didn't complete within %s (%v)", res, budget, err)
	}
	return nil
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		s = s[i+2:]
	}
	return truncate(s, 120)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
