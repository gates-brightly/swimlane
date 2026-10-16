package wait

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

func run(t *testing.T, args ...string) (*gomodtest.Fake, error) {
	t.Helper()
	f := gomodtest.NewFake(t.TempDir(), map[string]string{}, false, args...)
	return f, Run(&f.Ctx)
}

func TestWaitHTTP(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	f, err := run(t, "http", srv.URL+"/health", "--status", "200,204", "--every", "10ms", "--for", "5s")
	if err != nil {
		t.Fatalf("%v\n%s", err, f.StdoutB.String())
	}
	out := f.StdoutB.String()
	for _, want := range []string{"not yet (attempt 1", "HTTP 503", "ready after", "attempt 3: HTTP 204"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "HTTP 503") != 1 {
		t.Errorf("an unchanged state was repeated:\n%s", out)
	}
	// Never ready: it gives up after --for, saying the last state.
	n.Store(-1000)
	if _, err := run(t, "http", srv.URL, "--every", "10ms", "--for", "100ms"); err == nil || !strings.Contains(err.Error(), "not ready after 100ms") || !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("give up: %v", err)
	}
	if _, err := run(t, "http", srv.URL, "--status", "abc"); err == nil {
		t.Error("a bad --status passed")
	}
}

func TestWaitTCPAndCmd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if f, err := run(t, "tcp", ln.Addr().String(), "--every", "10ms", "--for", "2s"); err != nil {
		t.Fatalf("%v %s", err, f.StdoutB.String())
	}
	addr := ln.Addr().String()
	ln.Close()
	if _, err := run(t, "tcp", addr, "--every", "10ms", "--for", "50ms"); err == nil {
		t.Error("a closed port was ready")
	}

	dir := t.TempDir()
	flag := filepath.Join(dir, "ready")
	go func() { time.Sleep(100 * time.Millisecond); os.WriteFile(flag, nil, 0o644) }()
	f, err := run(t, "cmd", "--every", "20ms", "--for", "5s", "--", "test", "-e", flag)
	if err != nil {
		t.Fatalf("%v\n%s", err, f.StdoutB.String())
	}
	if !strings.Contains(f.StdoutB.String(), "exit 1") || !strings.Contains(f.StdoutB.String(), "ready after") {
		t.Errorf("cmd: %s", f.StdoutB.String())
	}
	if _, err := run(t, "cmd", "test"); err == nil {
		t.Error("cmd without -- passed")
	}
}

func TestWaitKubeRollout(t *testing.T) {
	f := gomodtest.NewFake(t.TempDir(), map[string]string{}, false, "kube-rollout", "deploy/orders", "--namespace", "shop", "--for", "2m")
	f.Tool("kubectl", `echo "kubectl $*"`)
	if err := Run(&f.Ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.StdoutB.String(), "kubectl rollout status deploy/orders --timeout=2m0s --namespace shop") {
		t.Errorf("kubectl call: %s", f.StdoutB.String())
	}
}
