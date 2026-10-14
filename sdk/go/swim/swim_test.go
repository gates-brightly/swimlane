package swim

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSwim serves the lane protocol from a script of answers, recording
// every request, so the SDK can be tested without swim.
type fakeSwim struct {
	mu   sync.Mutex
	reqs []map[string]any
	// answer returns the response for a request (id and ok are filled in).
	answer func(op string, args map[string]any) map[string]any
}

func serve(t *testing.T, answer func(op string, args map[string]any) map[string]any) *fakeSwim {
	t.Helper()
	dir, err := os.MkdirTemp("", "swimsdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	t.Setenv("SWIM_SOCKET", path)
	t.Setenv("SWIM_TOKEN", "tok")
	f := &fakeSwim{answer: answer}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var req map[string]any
					json.Unmarshal(sc.Bytes(), &req)
					f.mu.Lock()
					f.reqs = append(f.reqs, req)
					f.mu.Unlock()
					args, _ := req["args"].(map[string]any)
					resp := map[string]any{"ok": true, "result": map[string]any{}}
					if req["token"] != "tok" {
						resp = map[string]any{"ok": false, "error": "wrong token"}
					} else if a := f.answer(req["op"].(string), args); a != nil {
						for k, v := range a {
							resp[k] = v
						}
					}
					resp["id"] = req["id"]
					b, _ := json.Marshal(resp)
					c.Write(append(b, '\n'))
				}
			}()
		}
	}()
	return f
}

func (f *fakeSwim) ops() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r["op"].(string))
	}
	return strings.Join(out, " ")
}

func TestRoundEndsAtGate(t *testing.T) {
	f := serve(t, func(op string, args map[string]any) map[string]any {
		switch op {
		case "run":
			return map[string]any{"result": map[string]any{"passed": true}}
		case "gate":
			return map[string]any{"round_ended": true, "code": 1, "result": map[string]any{"passed": false}}
		case "guard":
			return map[string]any{"result": map[string]any{"approved": args["flag"] == "YES"}}
		case "outcome":
			return map[string]any{"result": map[string]any{"outcome": "stopped"}}
		}
		return nil
	})
	var handled []string
	afterGate := false
	code := Round(func(r *R) error {
		if r.Info == nil {
			t.Error("no hello info")
		}
		r.OnSuccess(func(map[string]any) error { handled = append(handled, "success"); return nil })
		r.OnFailure(func(out map[string]any) error {
			handled = append(handled, "failure:"+out["outcome"].(string))
			return nil
		})
		r.OnExit(func(map[string]any) error { handled = append(handled, "exit"); return nil })
		if ok, err := r.Run("probe", "true"); !ok || err != nil {
			t.Errorf("run: %v %v", ok, err)
		}
		if ok, _ := r.Guard("YES", "do it"); !ok {
			t.Error("guard YES not approved")
		}
		if ok, _ := r.Guard("NO", "don't"); ok {
			t.Error("guard NO approved")
		}
		if _, err := r.Gate("must", "false"); err != nil {
			var ended *RoundEnded
			if !errors.As(err, &ended) || ended.Code != 1 {
				t.Errorf("gate error: %v", err)
			}
			return err
		}
		afterGate = true
		return nil
	})
	if code != 1 || afterGate {
		t.Errorf("code %d, after gate %v", code, afterGate)
	}
	if strings.Join(handled, ",") != "failure:stopped,exit" {
		t.Errorf("handlers: %v", handled)
	}
	if got := f.ops(); got != "hello run guard guard gate outcome handler handler handler handler" {
		t.Errorf("ops: %s", got)
	}
}

func TestInProcessStep(t *testing.T) {
	var ended []map[string]any
	var mu sync.Mutex
	serve(t, func(op string, args map[string]any) map[string]any {
		switch op {
		case "begin_step":
			return map[string]any{"result": map[string]any{"step": 7.0, "started": true}}
		case "end_step":
			mu.Lock()
			ended = append(ended, args)
			mu.Unlock()
		}
		return nil
	})
	code := Round(func(r *R) error {
		if err := r.Step("ok step", false, func(s *S) error { s.Log("items: 0"); return nil }); err != nil {
			return err
		}
		if err := r.Step("failed step", false, func(s *S) error { return s.Fail("table still has items") }); err != nil {
			t.Errorf("a failed non-gate step returned %v", err)
		}
		boom := errors.New("boom")
		if err := r.Step("broken", false, func(s *S) error { return boom }); !errors.Is(err, boom) {
			t.Errorf("an unexpected error should come back: %v", err)
		}
		return nil
	})
	if code != 0 {
		t.Errorf("code %d", code)
	}
	if len(ended) != 3 || ended[0]["ok"] != true || ended[1]["ok"] != false || ended[1]["message"] != "table still has items" || ended[2]["message"] != "boom" {
		t.Errorf("end_step calls: %v", ended)
	}
}

func TestOutsideARound(t *testing.T) {
	t.Setenv("SWIM_SOCKET", "")
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "swim run N") {
		t.Errorf("Open outside a round: %v", err)
	}
	serve(t, nil)
	t.Setenv("SWIM_TOKEN", "wrong")
	if _, err := Open(); err == nil || !strings.Contains(err.Error(), "wrong token") {
		t.Errorf("a wrong token: %v", err)
	}
}
