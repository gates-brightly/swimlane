// Package swim gives compiled lane programs (Go) swim's steps as calls,
// over the lane protocol swim.lane/v1 (proto/swim/lane/v1/README.md).
//
//	func main() {
//		os.Exit(swim.Round(func(r *swim.R) error {
//			if err := r.Stage("check"); err != nil {
//				return err
//			}
//			if _, err := r.Gate("tf plan", "terraform", "plan", "-detailed-exitcode"); err != nil {
//				return err
//			}
//			return r.Step("orders table is empty", true, func(s *swim.S) error {
//				s.Log("items: 0")
//				return nil
//			})
//		}))
//	}
//
// A lane split into jobs ("// swim job 1.1" comments, each with its own
// header) runs once per job; Jobs calls that job's function.
//
// Secrets stored with swim secret set come by name:
// swim.Secret.Get("db_password").
//
// swim decides everything; this package only talks to it over the lane's
// socket. Every call returns *RoundEnded (check with errors.As) once the
// round is over: return it up, so the program unwinds and exits.
package swim

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// Protocol is the lane protocol this package speaks.
const Protocol = "swim.lane/v1"

// Version is this SDK's version.
const Version = "1.0.0"

// RoundEnded is returned once the round is over: a failed gate, stop, a
// refused step, an operator stop or a time limit.
type RoundEnded struct{ Code int }

func (e *RoundEnded) Error() string { return fmt.Sprintf("swim: the round ended (exit %d)", e.Code) }

// R is the round swim started for this lane.
type R struct {
	conn     net.Conn
	rd       *bufio.Reader
	token    string
	id       int
	Info     map[string]any
	handlers map[string]func(outcome map[string]any) error
}

// Open connects to the lane's round.
func Open() (*R, error) {
	path := os.Getenv("SWIM_SOCKET")
	if path == "" {
		return nil, errors.New("swim: not inside a round (SWIM_SOCKET is unset): run this lane with `swim run N`")
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	r := &R{conn: c, rd: bufio.NewReader(c), token: os.Getenv("SWIM_TOKEN"), handlers: map[string]func(map[string]any) error{}}
	r.Info, err = r.call("hello", map[string]any{"protocol": Protocol, "sdk": "go " + Version})
	if err != nil {
		c.Close()
		return nil, err
	}
	return r, nil
}

// Close ends the connection.
func (r *R) Close() error { return r.conn.Close() }

// Job is this round's job in a lane of jobs ("1.5.9"), or "".
func (r *R) Job() string {
	s, _ := r.Info["job_path"].(string)
	return s
}

func (r *R) call(op string, args map[string]any) (map[string]any, error) {
	r.id++
	req, _ := json.Marshal(map[string]any{"id": r.id, "token": r.token, "op": op, "args": args})
	if _, err := r.conn.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	line, err := r.rd.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("swim closed the lane's socket: %w", err)
	}
	var resp struct {
		OK         bool           `json:"ok"`
		Error      string         `json:"error"`
		RoundEnded bool           `json:"round_ended"`
		Code       int            `json:"code"`
		Result     map[string]any `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("swim: %s", resp.Error)
	}
	if resp.RoundEnded && op != "outcome" && op != "handler" {
		return resp.Result, &RoundEnded{Code: resp.Code}
	}
	if resp.Result == nil {
		resp.Result = map[string]any{}
	}
	return resp.Result, nil
}

// Opts are a step's options, as for swim run: Timeout "2m", Retry 3, ...
type Opts struct {
	Timeout, Backoff string
	Retry            int
	RetryOn          []int
	Soft             bool
}

func (o Opts) flags() []string {
	var out []string
	if o.Timeout != "" {
		out = append(out, "--timeout", o.Timeout)
	}
	if o.Retry > 0 {
		out = append(out, "--retry", fmt.Sprint(o.Retry))
	}
	if o.Backoff != "" {
		out = append(out, "--backoff", o.Backoff)
	}
	if len(o.RetryOn) > 0 {
		codes := make([]string, len(o.RetryOn))
		for i, c := range o.RetryOn {
			codes[i] = fmt.Sprint(c)
		}
		out = append(out, "--retry-on", strings.Join(codes, ","))
	}
	if o.Soft {
		out = append(out, "--soft")
	}
	return out
}

// ModuleOpts are a module call's options: a step's, plus its label, Gate
// (a failure ends the round) and Env (take the variables it gives).
type ModuleOpts struct {
	Opts
	Label    string
	Gate     bool
	Evidence bool // its output is the round's evidence (tf plan)
	Env      bool
}

// Module runs a command of a module the lane declares (# Modules:) as a
// step: true if it passed.
func (r *R) Module(name string, args ...string) (bool, error) {
	return r.ModuleWith(ModuleOpts{}, name, args...)
}

// ModuleEnv is Module for a module that gives the lane variables
// (credentials): they're set in this program's environment and every
// later step's, masked.
func (r *R) ModuleEnv(name string, args ...string) (bool, error) {
	return r.ModuleWith(ModuleOpts{Env: true}, name, args...)
}

// ModuleWith is Module with options.
func (r *R) ModuleWith(o ModuleOpts, name string, args ...string) (bool, error) {
	if args == nil {
		args = []string{}
	}
	res, err := r.call("module", map[string]any{"name": name, "args": args, "opts": o.flags(), "label": o.Label, "gate": o.Gate, "evidence": o.Evidence, "env": o.Env})
	if env, ok := res["env"].(map[string]any); ok {
		for k, v := range env {
			if s, ok := v.(string); ok {
				os.Setenv(k, s)
			}
		}
	}
	passed, _ := res["passed"].(bool)
	return passed, err
}

func (r *R) step(op, label string, o Opts, argv []string) (bool, error) {
	res, err := r.call(op, map[string]any{"label": label, "argv": argv, "opts": o.flags()})
	passed, _ := res["passed"].(bool)
	return passed, err
}

// Run is a step: whether it passed. A failure doesn't stop the round.
func (r *R) Run(label string, argv ...string) (bool, error) {
	return r.step("run", label, Opts{}, argv)
}

// RunWith is Run with options.
func (r *R) RunWith(label string, o Opts, argv ...string) (bool, error) {
	return r.step("run", label, o, argv)
}

// Gate is a step whose failure ends the round (*RoundEnded).
func (r *R) Gate(label string, argv ...string) (bool, error) {
	return r.step("gate", label, Opts{}, argv)
}

// Snapshot is a read-only step whose output is saved.
func (r *R) Snapshot(label string, argv ...string) (bool, error) {
	return r.step("snapshot", label, Opts{}, argv)
}

// Evidence is a check step whose output an approval can cover.
func (r *R) Evidence(label string, argv ...string) (bool, error) {
	return r.step("evidence", label, Opts{}, argv)
}

// S is an in-process step.
type S struct {
	r       *R
	id      int
	message string
}

// Log adds a line to the step's output (masked by swim).
func (s *S) Log(text string) {
	s.r.call("step_output", map[string]any{"step": s.id, "text": text})
}

// Fail makes the step fail with message (return its result from the step).
func (s *S) Fail(message string) error {
	s.message = message
	return errStepFailed
}

var errStepFailed = errors.New("step failed")

// Step runs fn in-process as a step labelled label: it passes if fn returns
// nil. A failed gate step ends the round.
func (r *R) Step(label string, gate bool, fn func(s *S) error) error {
	res, err := r.call("begin_step", map[string]any{"label": label, "gate": gate})
	if err != nil {
		return err
	}
	if started, _ := res["started"].(bool); !started {
		return &RoundEnded{Code: 130}
	}
	id, _ := res["step"].(float64)
	s := &S{r: r, id: int(id)}
	ferr := fn(s)
	var ended *RoundEnded
	if errors.As(ferr, &ended) {
		return ferr
	}
	msg := s.message
	if ferr != nil && msg == "" {
		msg = ferr.Error()
	}
	if _, err := r.call("end_step", map[string]any{"step": s.id, "ok": ferr == nil, "message": msg}); err != nil {
		return err
	}
	if ferr != nil && !errors.Is(ferr, errStepFailed) {
		return ferr
	}
	return nil
}

func (r *R) simple(op string, args map[string]any) error {
	_, err := r.call(op, args)
	return err
}

// Stage starts a stage: snapshot, check, change or verify.
func (r *R) Stage(name string) error { return r.simple("stage", map[string]any{"name": name}) }

// Guard reports whether the operator approves action; false is a dry run.
func (r *R) Guard(flag, action string) (bool, error) {
	res, err := r.call("guard", map[string]any{"flag": flag, "action": action})
	ok, _ := res["approved"].(bool)
	return ok, err
}

// Confirm asks the operator; no ends the round.
func (r *R) Confirm(question string) (bool, error) {
	res, err := r.call("confirm", map[string]any{"question": question})
	ok, _ := res["confirmed"].(bool)
	return ok, err
}

// Drift records DRIFT.
func (r *R) Drift(what string) error { return r.simple("drift", map[string]any{"what": what}) }

// Stop records STOP and ends the round.
func (r *R) Stop(reason string) error { return r.simple("stop", map[string]any{"reason": reason}) }

// Msg posts the lane's status message (progress "7/25", or "").
func (r *R) Msg(text, progress string) error {
	return r.simple("msg", map[string]any{"text": text, "progress": progress})
}

// Input is the path (or small value) of a dependency's output.
func (r *R) Input(ref, name string) (string, error) {
	res, err := r.call("input", map[string]any{"ref": ref, "name": name})
	v, _ := res["value"].(string)
	return v, err
}

// Output records a small value among the round's outputs.
func (r *R) Output(key, value string) error {
	return r.simple("output", map[string]any{"key": key, "value": value})
}

// OnSuccess, OnFailure and OnExit register the round's outcome handlers,
// run by Round as the round closes.
func (r *R) OnSuccess(fn func(outcome map[string]any) error) { r.handlers["on_success"] = fn }
func (r *R) OnFailure(fn func(outcome map[string]any) error) { r.handlers["on_failure"] = fn }
func (r *R) OnExit(fn func(outcome map[string]any) error)    { r.handlers["on_exit"] = fn }

func (r *R) runHandlers() {
	if len(r.handlers) == 0 {
		return
	}
	out, err := r.call("outcome", nil)
	if err != nil {
		return
	}
	first := "on_failure"
	if out["outcome"] == "pass" {
		first = "on_success"
	}
	for _, name := range []string{first, "on_exit"} {
		fn := r.handlers[name]
		if fn == nil {
			continue
		}
		r.call("handler", map[string]any{"name": name})
		if err := fn(out); err != nil {
			r.call("drift", map[string]any{"what": name + " returned " + err.Error()})
		}
		r.call("handler", map[string]any{"name": ""})
	}
}

// Jobs runs this lane's job: the function handlers has for it, called with
// the round, and returns the exit code for the program (as Round). Keys are
// job ids ("1.2") or their place under the lane ("2"); the closest one
// wins, so "1.5" handles 1.5.9 when nothing names 1.5.9 itself. A job with
// no function ends the round as failed.
//
//	os.Exit(swim.Jobs(map[string]func(*swim.R) error{
//		"1.1": fmt,
//		"1.2": lint,
//	}))
func Jobs(handlers map[string]func(r *R) error) int {
	return Round(func(r *R) error {
		job := r.Job()
		if job == "" {
			return errors.New(`this lane has no jobs (no "// swim job N.K" lines): use swim.Round`)
		}
		fn := jobHandler(handlers, job)
		if fn == nil {
			keys := make([]string, 0, len(handlers))
			for k := range handlers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return r.Stop(fmt.Sprintf("no function for job %s (Jobs has %s)", job, strings.Join(keys, ", ")))
		}
		return fn(r)
	})
}

func jobHandler(handlers map[string]func(r *R) error, job string) func(r *R) error {
	lane, path, _ := strings.Cut(job, ".")
	parts := strings.Split(path, ".")
	for k := len(parts); k > 0; k-- {
		p := strings.Join(parts[:k], ".")
		for _, key := range []string{lane + "." + p, p} {
			if fn, ok := handlers[key]; ok {
				return fn
			}
		}
	}
	return nil
}

// Round runs fn as the lane's round and returns the exit code for the
// program: 0, the round's code if it ended, or 1 for another error.
func Round(fn func(r *R) error) int {
	r, err := Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer r.Close()
	ferr := fn(r)
	r.runHandlers()
	var ended *RoundEnded
	switch {
	case ferr == nil:
		return 0
	case errors.As(ferr, &ended):
		return ended.Code
	}
	fmt.Fprintln(os.Stderr, "swim:", ferr)
	return 1
}
