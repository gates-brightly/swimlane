package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func admissionHub(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := NewWithState("sekret", "adm1n", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

// Anyone may enroll, token or not; nothing gets in until the admin admits it.
func TestAdmission(t *testing.T) {
	s, srv := admissionHub(t)
	w := Worker{Name: "stranger", Host: "laptop", Root: "/src/x", Namespace: "github.com/org/x"}
	c := NewClient(srv.URL, "", "")
	id, err := c.Enroll(w)
	if err != nil || id == "" || c.WorkerToken == "" || !c.Pending {
		t.Fatalf("an open enrollment: %q %v pending=%v", id, err, c.Pending)
	}
	// With the enroll token it waits too.
	tok := NewClient(srv.URL, "sekret", "")
	if _, err := tok.Enroll(Worker{Name: "tokened", Host: "laptop", Root: "/src/y"}); err != nil || !tok.Pending {
		t.Fatalf("enrolling with the token: %v pending=%v", err, tok.Pending)
	}
	// Waiting: on the admin's list, nowhere else, and refused everything.
	if p := s.Pending(); len(p) != 2 || p[0].Name != "stranger" || p[0].Namespace != "github.com/org/x" || p[0].From == "" {
		t.Fatalf("pending: %+v", p)
	}
	if body := get(t, srv.URL+"/api/enrollments"); strings.Contains(body, "stranger") {
		t.Errorf("a waiting worker is listed: %s", body)
	}
	if err := c.Report(Snapshot{Worker: w, Activity: "idle"}); err == nil || !strings.Contains(err.Error(), "waiting for the hub's admin") {
		t.Errorf("a waiting worker's report: %v", err)
	}
	if _, _, err := c.Poll(t.Context(), 0, "", 0); err == nil {
		t.Error("a waiting worker polled for commands")
	}
	if _, err := c.FetchGrants(); err == nil {
		t.Error("a waiting worker fetched grants")
	}
	user, _ := s.AddUser("op", RoleOperator)
	u, _ := NewUser(srv.URL, user, nil)
	if _, err := u.Send(id, Command{Kind: KindDrain}); err == nil {
		t.Error("a command was sent to a waiting worker")
	}
	// Nobody else takes its place, or knocks it back; its own token does.
	if _, err := NewClient(srv.URL, "", "").Enroll(w); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("another enrollment for its directory: %v", err)
	}
	if _, err := NewClient(srv.URL, "sekret", "").Enroll(w); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("the enroll token taking its place: %v", err)
	}
	if _, err := c.Enroll(w); err != nil || !c.Pending {
		t.Errorf("its own token enrolling again: %v pending=%v", err, c.Pending)
	}
	// Admitted: it reports and shows.
	if _, err := s.Admit(id, "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Report(Snapshot{Worker: w, Activity: "idle"}); err != nil {
		t.Fatalf("an admitted worker's report: %v", err)
	}
	if body := get(t, srv.URL+"/api/enrollments"); !strings.Contains(body, `"name":"stranger"`) {
		t.Errorf("an admitted worker isn't listed: %s", body)
	}
	if _, err := c.Enroll(w); err != nil || c.Pending {
		t.Errorf("enrolling again once admitted: %v pending=%v", err, c.Pending)
	}
	if _, err := NewClient(srv.URL, "", "").Enroll(w); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("taking an admitted worker's place: %v", err)
	}
	// Turned away: forgotten, and may enroll (and wait) again.
	if _, err := s.Reject(tok.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reject(id, "test"); err == nil {
		t.Error("an admitted worker was turned away as if waiting")
	}
	again := NewClient(srv.URL, "", "")
	if _, err := again.Enroll(Worker{Name: "tokened", Host: "laptop", Root: "/src/y"}); err != nil || !again.Pending {
		t.Errorf("enrolling again after being turned away: %v", err)
	}
	// The admin's enrollment is its admission.
	adm := NewClient(srv.URL, "adm1n", "")
	if _, err := adm.Enroll(Worker{Name: "mine", Host: "spiff", Root: "/srv/m"}); err != nil || adm.Pending {
		t.Errorf("the admin enrolling: %v pending=%v", err, adm.Pending)
	}
	// Admission survives a restart; waiting workers stay off the pages.
	s2, err := NewWithState("sekret", "adm1n", s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if p := s2.Pending(); len(p) != 1 || p[0].Name != "tokened" {
		t.Errorf("pending after a restart: %+v", p)
	}
	for _, v := range s2.Workers() {
		if v.Name == "tokened" {
			t.Error("a waiting worker shows after a restart")
		}
	}
}

func TestAdmissionLimitAndAPI(t *testing.T) {
	s, srv := admissionHub(t)
	for i := 0; i < MaxPending; i++ {
		if _, err := NewClient(srv.URL, "", "").Enroll(Worker{Name: "w", Host: "h", Root: fmt.Sprintf("/r%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewClient(srv.URL, "", "").Enroll(Worker{Name: "w", Host: "h", Root: "/one-too-many"}); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("past the limit: %v", err)
	}
	call := func(method, path, tok string) int {
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
	op, _ := s.AddUser("op", RoleOperator)
	id := s.Pending()[0].ID
	if c := call("GET", "/api/pending", ""); c != http.StatusUnauthorized {
		t.Errorf("nobody listing the waiting: %d", c)
	}
	if c := call("POST", "/api/workers/"+id+"/admit", op); c != http.StatusForbidden {
		t.Errorf("an operator admitting: %d", c)
	}
	if c := call("POST", "/api/workers/"+id+"/admit", "adm1n"); c != 200 {
		t.Errorf("the admin token admitting: %d", c)
	}
	a := NewAdmin(srv.URL, "adm1n")
	p, err := a.Pending()
	if err != nil || len(p) != MaxPending-1 {
		t.Fatalf("Admin.Pending: %d %v", len(p), err)
	}
	if _, err := a.Admit(p[0].ID, true); err != nil {
		t.Errorf("Admin.Admit reject: %v", err)
	}
	// Room again.
	if _, err := NewClient(srv.URL, "", "").Enroll(Worker{Name: "w", Host: "h", Root: "/one-too-many"}); err != nil {
		t.Errorf("with room again: %v", err)
	}
}

// A hub without state has nowhere to keep admissions: the enroll token
// enrolls and admits, as before.
func TestNoStateNoAdmission(t *testing.T) {
	srv := httptest.NewServer(New("sekret").Handler())
	defer srv.Close()
	if _, err := NewClient(srv.URL, "", "").Enroll(Worker{Name: "w", Host: "h", Root: "/r"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("without the token: %v", err)
	}
	c := NewClient(srv.URL, "sekret", "")
	if _, err := c.Enroll(Worker{Name: "w", Host: "h", Root: "/r"}); err != nil || c.Pending {
		t.Errorf("with the token: %v pending=%v", err, c.Pending)
	}
}
