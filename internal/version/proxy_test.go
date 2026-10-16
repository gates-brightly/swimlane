package version

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The proxy's @latest, from GOPROXY's first http(s) entry; behind the
// newest tag, it's said with the line that installs the tag by name.
func TestProxyLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+ModulePath+"/@latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"Version":"v0.20261016.6","Time":"2026-10-16T12:00:00Z"}`))
	}))
	defer srv.Close()
	t.Setenv("GOPROXY", srv.URL+",direct")
	if v, ok := ProxyLatest(time.Second); !ok || v != "v0.20261016.6" {
		t.Fatalf("ProxyLatest = %q %v", v, ok)
	}
	t.Setenv("GOPROXY", "off")
	if _, ok := ProxyLatest(time.Second); ok {
		t.Error("GOPROXY=off asked a proxy")
	}
	note := ProxyBehind("v0.20261016.8", "v0.20261016.6")
	if !strings.Contains(note, "still gives v0.20261016.6") || !strings.Contains(note, "go install "+ModulePath+"/cmd/swim@v0.20261016.8") {
		t.Errorf("behind: %q", note)
	}
	for _, c := range [][2]string{{"v0.20261016.6", "v0.20261016.6"}, {"v0.20261016.6", "v0.20261017.1"}, {"v0.20261016.6", "not-a-tag"}} {
		if note := ProxyBehind(c[0], c[1]); note != "" {
			t.Errorf("ProxyBehind(%s, %s) = %q", c[0], c[1], note)
		}
	}
	if ProxyBehind("v0.20261016.10", "v0.20261016.9") == "" {
		t.Error("index 9 behind 10")
	}
}
