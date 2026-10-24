package hub

import (
	"net/http"
	"testing"
)

// An admin user does what the admin token does, by token or by session.
func TestAdminRole(t *testing.T) {
	s, srv, worker, alice, _ := commandHub(t)
	adminTok, err := NewAdmin(srv.URL, "adm1n").AddUser("root", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, tok string) int {
		t.Helper()
		r, _ := http.NewRequest(method, srv.URL+path, nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, path := range []string{"/api/users", "/api/grants"} {
		if c := call("GET", path, adminTok); c != 200 {
			t.Errorf("an admin user's token, GET %s: %d", path, c)
		}
		if c := call("GET", path, alice); c != http.StatusForbidden {
			t.Errorf("an operator's token, GET %s: %d", path, c)
		}
		if c := call("GET", path, ""); c != http.StatusUnauthorized {
			t.Errorf("nobody, GET %s: %d", path, c)
		}
	}
	// An admin approves a worker's key, as the admin token would.
	if _, err := worker.Enroll(Worker{Name: "billing", Host: "box", Root: "/r", Key: "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"}); err != nil {
		t.Fatal(err)
	}
	if c := call("POST", "/api/workers/"+worker.ID+"/approve", adminTok); c != 200 {
		t.Errorf("an admin user approving a worker: %d", c)
	}
	if c := call("POST", "/api/workers/"+worker.ID+"/approve", alice); c != http.StatusForbidden {
		t.Errorf("an operator approving a worker: %d", c)
	}
	// And is an operator too: it may send commands.
	if !covers(RoleAdmin, RoleOperator) || !covers(RoleAdmin, RoleViewer) || covers(RoleOperator, RoleAdmin) || covers(RoleViewer, RoleOperator) {
		t.Error("covers: an admin covers operator and viewer, nothing covers admin but admin")
	}
	if _, err := s.AddUser("x", "root"); err == nil {
		t.Error("a role that isn't one was accepted")
	}
}
