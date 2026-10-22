package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNamespaceFor(t *testing.T) {
	for _, c := range []struct{ origin, root, want string }{
		{"git@github.com:org/api.git", "/src/api", "github.com/org/api"},
		{"https://github.com/org/api", "/src/api", "github.com/org/api"},
		{"https://zach:tok@Forgejo.Example.ts.net:3000/zach/swimlane.git", "/x", "forgejo.example.ts.net/zach/swimlane"},
		{"ssh://git@host.example:2222/a/b/c.git/", "/x", "host.example/a/b/c"},
		{"/srv/git/billing.git", "/home/me/billing", "billing"},
		{"file:///srv/git/billing.git", "/home/me/billing", "billing"},
		{"../upstream", "/home/me/thing", "thing"},
		{"", "/home/me/scratch", "scratch"},
		{"  git@github.com:org/api.git\n", "/x", "github.com/org/api"},
	} {
		if got := NamespaceFor(c.origin, c.root); got != c.want {
			t.Errorf("NamespaceFor(%q, %q) = %q, want %q", c.origin, c.root, got, c.want)
		}
	}
}

func TestCleanNamespace(t *testing.T) {
	for in, want := range map[string]string{" /github.com/org/api/ ": "github.com/org/api", "-": "", ".": ""} {
		if got := CleanNamespace(in); got != want {
			t.Errorf("CleanNamespace(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CleanNamespace("evil\x1b[31mred\u202e"); strings.ContainsAny(got, "\x1b\u202e") {
		t.Errorf("escape or bidi override kept: %q", got)
	}
	if got := CleanNamespace(strings.Repeat("a", 300)); len(got) != 200 {
		t.Errorf("not cut to 200: %d", len(got))
	}
}

func TestNamespaceTabs(t *testing.T) {
	s := New("sekret")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	enroll := func(name, host, ns string) *Client {
		c := NewClient(srv.URL, "sekret", "")
		w := Worker{Name: name, Host: host, Root: "/src/" + name, Namespace: ns}
		if _, err := c.Enroll(w); err != nil {
			t.Fatal(err)
		}
		if err := c.Report(Snapshot{Worker: w, Activity: "idle", Interval: 2, Lanes: []Lane{{Lane: 1, State: "running"}}}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	enroll("api-mac", "mac", "github.com/org/api")
	spiff := enroll("api-spiff", "spiff", "github.com/org/api")
	enroll("billing", "mac", "github.com/org/billing")
	enroll("old", "mac", "") // a swim from before namespaces

	// A report without a namespace keeps the one it enrolled with.
	if err := spiff.Report(Snapshot{Worker: Worker{Name: "api-spiff", Host: "spiff", Root: "/src/api-spiff"}, Activity: "idle", Interval: 2}); err != nil {
		t.Fatal(err)
	}

	names := func(q string) []string {
		resp, err := http.Get(srv.URL + "/api/enrollments" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got struct {
			Workers []View `json:"enrollments"`
		}
		json.NewDecoder(resp.Body).Decode(&got)
		var out []string
		for _, v := range got.Workers {
			out = append(out, v.Name)
		}
		return out
	}
	if got := strings.Join(names("?ns=github.com/org/api"), ","); got != "api-mac,api-spiff" {
		t.Errorf("?ns=github.com/org/api: %s", got)
	}
	if got := strings.Join(names("?ns=-"), ","); got != "old" {
		t.Errorf("?ns=-: %s", got)
	}
	if got := len(names("")); got != 4 {
		t.Errorf("all: %d workers", got)
	}

	tb := tabs(s.Workers(), "github.com/org/billing")
	var labels []string
	for _, x := range tb {
		labels = append(labels, x.Label)
	}
	if strings.Join(labels, ",") != "all,github.com/org/api,github.com/org/billing,no namespace" {
		t.Fatalf("tabs: %v", labels)
	}
	if tb[0].Workers != 4 || tb[1].Workers != 2 || tb[1].Running != 1 || !tb[2].Active || tb[0].Active {
		t.Errorf("tab counts: %+v", tb)
	}

	page := get(t, srv.URL+"/classic?ns=github.com/org/billing")
	if !strings.Contains(page, `class="on">github.com/org/billing`) || strings.Contains(page, "api-mac") || !strings.Contains(page, "billing") {
		t.Errorf("billing tab:\n%s", page)
	}
	if all := get(t, srv.URL+"/classic"); !strings.Contains(all, "api-mac") || !strings.Contains(all, `href="/classic?ns=github.com%2forg%2fapi"`) {
		t.Errorf("all tab lacks the workers or the namespace links:\n%s", all)
	}
	if empty := get(t, srv.URL+"/classic?ns=nobody/here"); !strings.Contains(empty, "No workers in nobody/here") {
		t.Errorf("empty namespace page:\n%s", empty)
	}
}
