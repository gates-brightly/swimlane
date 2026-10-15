package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/peer"
	"github.com/gates-brightly/swimlane/internal/redact"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/step"
	"github.com/gates-brightly/swimlane/internal/ui"
	"github.com/gates-brightly/swimlane/internal/version"
)

// The lane protocol, swim.lane/v1: how the native SDKs (sdk/python,
// sdk/ts, sdk/go) give a lane program swim's steps as calls. swim _wrap
// serves it on a per-lane Unix socket (SWIM_SOCKET, mode 0600) and checks a
// per-lane token (SWIM_TOKEN) on every request. Requests and responses are
// JSON, one per line; the contract is proto/swim/lane/v1/README.md.
//
// Every decision stays in swim: a run step is the in-lane command (swim
// run/gate/...) run by the wrapper, a guard asks swim guard, and so on, so
// there's one implementation. The SDK only maps answers to its language.

// LaneProtocol names the protocol and its major version.
const LaneProtocol = "swim.lane/v1"

// laneRequest is one call.
type laneRequest struct {
	ID    int             `json:"id"`
	Token string          `json:"token"`
	Op    string          `json:"op"`
	Args  json.RawMessage `json:"args,omitempty"`
}

// laneResponse is its answer. RoundEnded: the round is over (a failed gate,
// stop, a refused step, an operator stop, a time limit): the SDK raises
// RoundEnded and the program should unwind and exit (it is stopped after a
// grace period otherwise).
type laneResponse struct {
	ID         int            `json:"id"`
	OK         bool           `json:"ok"`
	Error      string         `json:"error,omitempty"`
	RoundEnded bool           `json:"round_ended,omitempty"`
	Code       int            `json:"code,omitempty"` // the round's exit code when it ended
	Reason     string         `json:"reason,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
}

// laneServer serves one lane's program.
type laneServer struct {
	root, bin, logPath string
	n                  int
	cfg                *config.Config
	env                []string // the program's environment, for the in-lane commands it runs
	token              string
	path               string
	ln                 net.Listener
	connected          atomic.Bool // an SDK said hello: end the round with a grace period
	inHandler          atomic.Bool
	mu                 sync.Mutex // one call at a time: a round's steps are sequential
	steps              map[int]*inStep
	nextStep           int
	mkOnce             sync.Once
	mk                 *redact.Masker // the lane's secrets (refreshed as guarded ones are released)
}

// masker masks what the program reports (step labels, output, details)
// before swim writes it anywhere: the log, status.yml, the hub.
func (s *laneServer) masker() *redact.Masker {
	s.mkOnce.Do(func() { s.mk = s.cfg.Masker(s.env) })
	return s.mk
}

// inStep is an in-process step: one the program runs itself (a native
// check), reporting its output and result.
type inStep struct {
	label  string
	gate   bool
	start  time.Time
	output strings.Builder
}

// startLaneServer listens on a fresh socket for lane n's program and
// returns the env it needs (SWIM_SOCKET, SWIM_TOKEN).
func startLaneServer(root, bin string, cfg *config.Config, n int, env []string) (*laneServer, []string, error) {
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, nil, err
	}
	// Short: macOS caps socket paths at 104 bytes.
	dir, err := os.MkdirTemp("", "swim-")
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.sock", n))
	ln, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, err
	}
	os.Chmod(path, 0o600)
	s := &laneServer{root: root, bin: bin, n: n, cfg: cfg, env: env, token: hex.EncodeToString(tok[:]), path: path, ln: ln,
		logPath: lane.Log(root, n), steps: map[int]*inStep{}}
	go s.serve()
	return s, []string{"SWIM_SOCKET=" + path, "SWIM_TOKEN=" + s.token}, nil
}

func (s *laneServer) close() {
	s.ln.Close()
	os.RemoveAll(filepath.Dir(s.path))
}

func (s *laneServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.conn(c)
	}
}

func (s *laneServer) conn(c net.Conn) {
	defer c.Close()
	// Only the lane's own program (this wrapper's descendants) may use its
	// socket, whoever else holds the token.
	if ok, known := peer.FromLane(c, os.Getpid()); known && !ok {
		json.NewEncoder(c).Encode(laneResponse{Error: "refused: the connection came from a process outside this lane"})
		return
	}
	enc := json.NewEncoder(c)
	readRequests(c, func(req laneRequest, err error) {
		if err != nil {
			enc.Encode(laneResponse{Error: "bad request: " + err.Error()})
			return
		}
		if req.Token != s.token {
			enc.Encode(laneResponse{ID: req.ID, Error: "wrong token"})
			return
		}
		s.mu.Lock()
		var resp laneResponse
		if c, ok := lane.EndCode(s.root, s.n); ok && startsStep[req.Op] && !s.inHandler.Load() {
			// The round has ended (its end response may not have reached
			// the program yet, or it ignored it): no step starts after it.
			resp = laneResponse{OK: true, RoundEnded: true, Code: c, Result: notRun(req.Op, c)}
		} else {
			resp = s.handle(req)
		}
		s.mu.Unlock()
		resp.ID = req.ID
		if c, ok := lane.EndCode(s.root, s.n); ok && !s.inHandler.Load() {
			resp.RoundEnded, resp.Code = true, c
		}
		enc.Encode(resp)
	})
}

// readRequests reads the protocol's framing from r: one JSON request per
// line, up to 8 MiB. Each line is handed to fn decoded, or with the error
// that kept it from decoding; it stops at the end of r or at a line over
// the limit.
func readRequests(r io.Reader, fn func(laneRequest, error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxRequest)
	for sc.Scan() {
		var req laneRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			fn(laneRequest{}, err)
			continue
		}
		fn(req, nil)
	}
	return sc.Err()
}

// maxRequest is the longest request line the lane socket reads.
const maxRequest = 8 * 1024 * 1024

// startsStep is the ops that start a step (or ask the operator), refused
// once the round's end is claimed.
var startsStep = map[string]bool{"run": true, "gate": true, "snapshot": true, "evidence": true, "begin_step": true, "guard": true, "confirm": true}

// notRun is the result of a step op refused because the round has ended.
func notRun(op string, code int) map[string]any {
	switch op {
	case "begin_step":
		return map[string]any{"started": false}
	case "guard":
		return map[string]any{"approved": false}
	case "confirm":
		return map[string]any{"confirmed": false}
	}
	return map[string]any{"exit": max(code, 1), "passed": false}
}

// args reads req's arguments into v.
func (req laneRequest) args(v any) error {
	if len(req.Args) == 0 {
		return nil
	}
	return json.Unmarshal(req.Args, v)
}

func fail(format string, a ...any) laneResponse {
	return laneResponse{Error: fmt.Sprintf(format, a...)}
}

func (s *laneServer) handle(req laneRequest) laneResponse {
	switch req.Op {
	case "hello":
		var a struct {
			Protocol string `json:"protocol"`
			SDK      string `json:"sdk"`
		}
		req.args(&a)
		if a.Protocol != "" && a.Protocol != LaneProtocol {
			return fail("this swim (%s) speaks %s, the SDK %s: update the SDK, or swim (%s)", version.String(), LaneProtocol, a.Protocol, version.InstallCmd)
		}
		s.connected.Store(true)
		deadline, _ := strconv.ParseInt(os.Getenv("SWIM_DEADLINE"), 10, 64)
		result := map[string]any{
			"protocol": LaneProtocol, "swim": version.String(), "syntax": version.Current,
			"lane": s.n, "cell": lane.CurrentCell(), "job": envOf(s.env, "SWIM_JOB"), "run": envOf(s.env, "SWIM_RUN"),
			"deadline": deadline, "check": lane.CheckMode(),
		}
		// A finally lane: how each lane it waited for ended (1: pass).
		if saw := finallySaw(); saw != nil {
			result["finally"] = saw
		}
		// One job of a lane of jobs: its id (1.5.9), for the SDK's jobs().
		if cell := lane.CurrentCell(); lane.IsJobPath(cell) {
			if _, ok := lane.LaneJobs(s.root, s.n); ok {
				result["job_path"] = fmt.Sprintf("%d.%s", s.n, cell)
			}
		}
		return laneResponse{OK: true, Result: result}
	case "run", "gate", "snapshot", "evidence":
		var a struct {
			Label string   `json:"label"`
			Argv  []string `json:"argv"`
			Opts  []string `json:"opts"` // --timeout 2m, --retry 3, --soft, ...
		}
		if err := req.args(&a); err != nil || a.Label == "" || len(a.Argv) == 0 {
			return fail("%s needs label and argv", req.Op)
		}
		args := append([]string{req.Op}, a.Opts...)
		args = append(append(args, a.Label, "--"), a.Argv...)
		code := s.swim(args...)
		return laneResponse{OK: true, Result: map[string]any{"exit": code, "passed": code == 0}}
	case "guard":
		var a struct{ Flag, Action string }
		if err := req.args(&a); err != nil || a.Flag == "" {
			return fail("guard needs flag and action")
		}
		code := s.swim("guard", a.Flag, a.Action)
		if code == 3 {
			// An Observe: fact changed between approval and now: the round
			// ends as drift, as a bash lane's guard ends it.
			s.swim("stop", "drift: observed facts changed since approval ("+a.Flag+")")
		}
		return laneResponse{OK: true, Result: map[string]any{"approved": code == 0}}
	case "confirm":
		var a struct{ Question string }
		req.args(&a)
		// A question for the operator, answered like a guard nobody can
		// approve with a flag; no means the round stops.
		code := s.swim("_approve", strconv.Itoa(s.n), "SWIM_CONFIRM", a.Question)
		if code != 0 {
			s.swim("stop", "not confirmed: "+a.Question)
		}
		return laneResponse{OK: true, Result: map[string]any{"confirmed": code == 0}}
	case "stage":
		var a struct{ Name string }
		req.args(&a)
		code := s.swim("stage", a.Name)
		return laneResponse{OK: code == 0, Error: map[bool]string{true: "", false: "bad stage " + a.Name}[code == 0]}
	case "drift":
		var a struct{ What string }
		req.args(&a)
		s.swim("drift", a.What)
		return laneResponse{OK: true}
	case "stop":
		var a struct{ Reason string }
		req.args(&a)
		s.swim("stop", a.Reason)
		return laneResponse{OK: true}
	case "msg":
		var a struct{ Text, Progress string }
		req.args(&a)
		args := []string{"msg"}
		if a.Progress != "" {
			args = append(args, "--progress", a.Progress)
		}
		s.swim(append(args, a.Text)...)
		return laneResponse{OK: true}
	case "input":
		var a struct{ Ref, Name string }
		req.args(&a)
		out, code := s.swimOut("input", a.Ref, a.Name)
		if code != 0 {
			return fail("input %s %s: not available (see the log)", a.Ref, a.Name)
		}
		return laneResponse{OK: true, Result: map[string]any{"value": strings.TrimRight(out, "\n")}}
	case "output":
		var a struct{ Key, Value string }
		req.args(&a)
		if code := s.swim("output", a.Key, a.Value); code != 0 {
			return fail("output %s: refused", a.Key)
		}
		return laneResponse{OK: true}
	case "begin_step":
		var a struct {
			Label string
			Gate  bool
		}
		if err := req.args(&a); err != nil || a.Label == "" {
			return fail("begin_step needs a label")
		}
		if !s.inHandler.Load() {
			holdIfPaused(s.root, s.n, envOf(s.env, "SWIM_RUN"), a.Label) // a paused round holds here
		}
		if lane.StopRequested(s.root, s.n, envOf(s.env, "SWIM_RUN")) && !s.inHandler.Load() {
			s.swim("stop", "interrupted by operator (before "+a.Label+")")
			return laneResponse{OK: true, Result: map[string]any{"started": false}}
		}
		s.nextStep++
		// The program's: masked (status.yml and the hub show it), and no
		// escape sequences or controls on screens.
		label := ui.SafeField(s.masker().String(a.Label))
		s.steps[s.nextStep] = &inStep{label: label, gate: a.Gate, start: time.Now()}
		status.Update(s.root, s.n, s.cfg.Lanes, func(l *status.Lane) { l.CurrentStep = label })
		return laneResponse{OK: true, Result: map[string]any{"step": s.nextStep, "started": true}}
	case "step_output":
		var a struct {
			Step int
			Text string
		}
		req.args(&a)
		st := s.steps[a.Step]
		if st == nil {
			return fail("no step %d", a.Step)
		}
		st.output.WriteString(a.Text)
		if !strings.HasSuffix(a.Text, "\n") {
			st.output.WriteString("\n")
		}
		fmt.Fprint(os.Stdout, a.Text)
		if !strings.HasSuffix(a.Text, "\n") {
			fmt.Fprintln(os.Stdout)
		}
		return laneResponse{OK: true}
	case "end_step":
		var a struct {
			Step    int
			OK      bool
			Message string
		}
		req.args(&a)
		st := s.steps[a.Step]
		if st == nil {
			return fail("no step %d", a.Step)
		}
		delete(s.steps, a.Step)
		return s.endStep(st, a.OK, a.Message)
	case "outcome":
		r, _ := logparse.ParseFile(s.logPath)
		code := 0
		if c, ok := lane.EndCode(s.root, s.n); ok {
			code = c
		}
		outcome, reason, fstep, fstage := wrappedOutcome(r, code)
		return laneResponse{OK: true, Result: map[string]any{"outcome": outcome, "stop_reason": reason, "failed_step": fstep, "failed_stage": fstage}}
	case "handler":
		var a struct{ Name string }
		req.args(&a)
		if a.Name == "" {
			s.inHandler.Store(false)
			return laneResponse{OK: true}
		}
		if code := s.swim("_handler", strconv.Itoa(s.n), a.Name); code != 0 {
			return fail("unknown handler %s", a.Name)
		}
		s.inHandler.Store(true)
		return laneResponse{OK: true}
	}
	return fail("unknown op %q (this swim speaks %s)", req.Op, LaneProtocol)
}

// endStep records an in-process step: its block in the log, marked
// (in-process), and its result in status; a failed gate ends the round.
func (s *laneServer) endStep(st *inStep, ok bool, message string) laneResponse {
	kind, detail := logparse.Pass, "in-process"
	exit := 0
	if !ok {
		kind, exit = logparse.Fail, 1
		detail = "in-process"
		if message != "" {
			detail += ", " + message
		}
	}
	mk := s.masker()
	out := mk.String(st.output.String())
	if _, err := step.RecordStep(s.logPath, kind, mk.String(st.label), mk.String(detail), "(in-process)", st.start, time.Since(st.start), strings.NewReader(out)); err != nil {
		return fail("%v", err)
	}
	line := logparse.ResultLine(kind, mk.String(st.label), mk.String(detail))
	status.Update(s.root, s.n, s.cfg.Lanes, func(l *status.Lane) {
		if ok {
			l.Pass++
		} else {
			l.Fail++
			l.FailedSteps = append(l.FailedSteps, line)
		}
	})
	if !ok && st.gate && !s.inHandler.Load() {
		if lane.ClaimEnd(s.root, s.n, 1) {
			cmdMark([]string{strconv.Itoa(s.n), logparse.Stop, "gate failed: " + st.label})
		}
		// The wrapper gives the program its grace period to unwind.
		syscall.Kill(os.Getpid(), syscall.SIGUSR1)
	}
	return laneResponse{OK: true, Result: map[string]any{"exit": exit, "passed": ok}}
}

// swim runs one in-lane command as the program would, with its env (and
// SWIM_IN_HANDLER inside a handler), and returns its exit code. Its output
// goes to the terminal.
func (s *laneServer) swim(args ...string) int {
	_, code := s.swimRun(false, args...)
	return code
}

func (s *laneServer) swimOut(args ...string) (string, int) { return s.swimRun(true, args...) }

func (s *laneServer) swimRun(capture bool, args ...string) (string, int) {
	c := exec.Command(s.bin, args...)
	c.Dir, c.Env, c.Stderr = s.root, s.env, os.Stderr
	if s.inHandler.Load() {
		c.Env = setEnv(c.Env, "SWIM_IN_HANDLER", "1")
	}
	// The in-lane commands signal the wrapper themselves to end a round;
	// with an SDK connected the wrapper gives the program a grace period.
	var out strings.Builder
	if capture {
		c.Stdout = &out
	} else {
		c.Stdout = os.Stdout
	}
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.ExitCode()
	}
	return out.String(), 1
}

func envOf(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}
