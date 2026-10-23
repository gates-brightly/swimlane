package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/gates-brightly/swimlane/internal/hub"
)

// swim auth: the machine logs in once and is admitted once; then every
// directory where swim watch runs enrolls itself, admitted, and logging
// out removes them all from the hub.
func TestAuthMachineEnrollment(t *testing.T) {
	long(t)
	a := newRepo(t, "")
	b := newRepo(t, "")
	home := t.TempDir()
	a.env = append(a.env, "HOME="+home)
	for _, kv := range a.env { // one machine, one user: one config directory
		if strings.HasPrefix(kv, "XDG_CONFIG_HOME=") || strings.HasPrefix(kv, "HOME=") {
			b.env = append(b.env, kv)
		}
	}
	url, stop := startHub(t, a, t.TempDir(), "127.0.0.1:0")
	t.Cleanup(stop)
	for _, r := range []*repo{a, b} {
		r := r
		t.Cleanup(func() {
			if pid := headlessPid(r); pid > 0 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		})
	}
	workers := func() []hub.View {
		resp, err := http.Get(url + "/api/enrollments")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Workers []hub.View `json:"enrollments"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Workers
	}

	// Not logged in: --headless needs a hub, and says how to get one.
	if out, code := a.swim("watch", "--headless", "--detach"); code == 0 || !strings.Contains(out, "swim auth login URL") {
		t.Errorf("headless with no hub: exit %d\n%s", code, out)
	}
	out := a.mustSwim("auth", "login", url, "--access", "readwrite", "--name", "box")
	if !strings.Contains(out, "waiting for the hub's admin to admit this machine") {
		t.Errorf("login:\n%s", out)
	}
	if fi, err := os.Stat(filepath.Join(home, ".config", "swim", "auth.yml")); err == nil {
		t.Errorf("auth.yml under HOME, not XDG_CONFIG_HOME: %v", fi.Name())
	}
	// Waiting: a directory can't join through it yet.
	if out, code := a.swim("watch", "--headless", "--detach"); code == 0 || !strings.Contains(out, "this machine waits for the hub's admin") {
		t.Errorf("headless through a waiting machine: exit %d\n%s", code, out)
	}
	a.admit(t, url, "box")
	contains(t, "status", a.mustSwim("auth", "status"), "state:   admitted", "access:  readwrite")

	// Both directories join as their workers start: no admit each.
	a.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	b.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	waitFor(t, func() bool { return len(workers()) == 2 })
	for _, w := range workers() {
		if w.Access != "readwrite" || w.Runner.AdmittedBy != "machine box" {
			t.Errorf("a directory joined through the machine: %+v", w.Runner)
		}
	}
	contains(t, "status", b.mustSwim("auth", "status"), "directories through it (2):", a.root, b.root)
	hy, _ := os.ReadFile(filepath.Join(b.root, ".swim", "hub.yml"))
	contains(t, "hub.yml", string(hy), "via: auth")

	// Logout: the hub forgets the machine and both directories; here they
	// forget it too.
	out = a.mustSwim("auth", "logout")
	contains(t, "logout", out, "the hub removed this machine and 2 directories", "2 here forgot it")
	if w := workers(); len(w) != 0 {
		t.Errorf("still on the hub after logout: %d", len(w))
	}
	for _, r := range []*repo{a, b} {
		if _, err := os.Stat(filepath.Join(r.root, ".swim", "hub.yml")); err == nil {
			t.Errorf("%s still has hub.yml", r.root)
		}
	}
	contains(t, "status after", a.mustSwim("auth", "status"), "not logged in to a hub")
	a.mustSwim("stop", "--all")
}

// The admin's side takes an admin user's token as well as the admin token;
// swim.yml's hub: {name, namespace} names a directory as it joins; and
// logging in again with another --access changes it for directories that
// joined already.
func TestAuthAdminUserNamesAndAccess(t *testing.T) {
	long(t)
	r := newRepo(t, "")
	r.env = append(r.env, "HOME="+t.TempDir())
	url, stop := startHub(t, r, t.TempDir(), "127.0.0.1:0")
	t.Cleanup(stop)

	out := r.mustSwim("auth", "users", "add", "root", "--role", "admin", "--hub", url, "--admin-token", "adm1n")
	tok := regexp.MustCompile(`(?m)^  ([0-9a-f]{48})$`).FindStringSubmatch(out)
	if tok == nil {
		t.Fatalf("no token in:\n%s", out)
	}
	contains(t, "admin login", r.mustSwim("auth", "login", url, "--token", tok[1]), "as its admin", "isn't a swimmer")
	contains(t, "login", r.mustSwim("auth", "login", url, "--access", "read", "--name", "box"), "waiting for the hub's admin")
	contains(t, "admit by an admin user", r.mustSwim("auth", "admit", "box"), "box admitted")
	contains(t, "status", r.mustSwim("auth", "status"), "admin:   logged in to "+url, "state:   admitted", "access:  read")

	r.write("swim.yml", "hub:\n  name: billing\n  namespace: acme/billing\n", 0o644)
	r.mustSwim("watch", "--once")
	var w struct {
		Workers []hub.View `json:"enrollments"`
	}
	resp, err := http.Get(url + "/api/enrollments")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&w)
	resp.Body.Close()
	if len(w.Workers) != 1 || w.Workers[0].Name != "billing" || w.Workers[0].Namespace != "acme/billing" || w.Workers[0].Access != "read" {
		t.Fatalf("joined as: %+v", w.Workers)
	}

	contains(t, "access", r.mustSwim("auth", "login", url, "--access", "readwrite"), "access is now readwrite")
	r.mustSwim("watch", "--once")
	resp, err = http.Get(url + "/api/enrollments")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&w)
	resp.Body.Close()
	if len(w.Workers) != 1 || w.Workers[0].Access != "readwrite" {
		t.Errorf("after logging in with readwrite: %+v", w.Workers)
	}
	r.mustSwim("auth", "logout", "--admin")
	contains(t, "status after", r.mustSwim("auth", "status"), "not logged in to a hub as a swimmer")
}
