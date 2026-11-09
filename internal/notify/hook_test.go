package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHook(t *testing.T) {
	// The root is a symlink, as macOS's temp dirs are (/var is
	// /private/var): $PWD is the root as named, like $SWIM_ROOT.
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	h := NewHook(HookConfig{Command: `printf '%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s' "$SWIM_EVENT" "$SWIM_RESULT" "$SWIM_TITLE" "$SWIM_BODY" "$SWIM_LANES" "$SWIM_BATCH" "$SWIM_REPO" "$SWIM_BRANCH" "$PWD" "$SWIM_PRIORITY" "$SWIM_TAGS" > note`,
		Root: root, Warn: func(why string) { t.Error(why) }})
	h.Send(Note{Event: EventEnd, Result: "failed", Title: "ops@fix · batch #4 failed", Body: "lane 1,2\n· 3s\x07", Lanes: []int{1, 2}, Batch: 4, Repo: "ops", Branch: "fix"})
	h.Wait()
	b, err := os.ReadFile(filepath.Join(root, "note"))
	if err != nil {
		t.Fatal(err)
	}
	// One line, no controls: a lane's text reaches the command as data.
	if got, want := string(b), "end|failed|ops@fix · batch #4 failed|lane 1,2 · 3s|1,2|4|ops|fix|"+root+"|high|x,ops"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	var none *Hook
	none.Send(Note{}) // a nil Hook runs nothing
	none.Wait()
	if NewHook(HookConfig{Command: "  ", Root: root}) != nil {
		t.Error("a blank command made a Hook")
	}
}

// notify_url alone POSTs the note as an ntfy message, and each send is
// recorded: the last one, and why it failed.
func TestHookURL(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	var mu sync.Mutex
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got, body = r, string(b)
		mu.Unlock()
		if r.URL.Path == "/denied" {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		}
	}))
	defer srv.Close()
	state := filepath.Join(t.TempDir(), "swim", "notify.json")
	h := NewHook(HookConfig{URL: srv.URL + "/zach", Root: t.TempDir(), State: state})
	if err := h.SendNow(Note{Event: EventAsk, Title: "infra · lane 3 needs you", Body: "approval of DEPLOY: deploy 2.4.1", Lanes: []int{3}, Repo: "infra", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	r, b := got, body
	mu.Unlock()
	if r.URL.Path != "/zach" || r.Method != http.MethodPost || b != "approval of DEPLOY: deploy 2.4.1" ||
		r.Header.Get("Title") != "infra · lane 3 needs you" || r.Header.Get("Priority") != "high" || r.Header.Get("Tags") != "question,infra" {
		t.Errorf("sent %s %s %q %v", r.Method, r.URL.Path, b, r.Header)
	}
	if s, err := LastSent(state); err != nil || s == nil || s.Error != "" || s.Title != "infra · lane 3 needs you" || s.Event != EventAsk {
		t.Errorf("recorded %+v, %v", s, err)
	}
	if a := r.Header.Get("Actions"); a != "" {
		t.Errorf("Actions without phone links: %q", a)
	}

	// A question with phone links: Approve and Decline buttons, each a POST
	// from the ntfy app, and when they stop working.
	expires := time.Date(2026, 11, 9, 14, 17, 0, 0, time.UTC)
	h.SendNow(Note{Event: EventAsk, Title: "t", Body: "b", ApproveURL: "http://100.64.0.1:4242/approve/aa",
		RejectURL: "http://100.64.0.1:4242/reject/bb", Expires: expires})
	mu.Lock()
	r = got
	mu.Unlock()
	if a, want := r.Header.Get("Actions"), "http, Approve, http://100.64.0.1:4242/approve/aa, method=POST, clear=true; http, Decline, http://100.64.0.1:4242/reject/bb, method=POST, clear=true"; a != want {
		t.Errorf("Actions = %q, want %q", a, want)
	}
	env := strings.Join(Note{ApproveURL: "a", RejectURL: "r", Expires: expires}.env("/", ""), "\n")
	for _, want := range []string{"SWIM_APPROVE_URL=a", "SWIM_REJECT_URL=r", "SWIM_EXPIRES=2026-11-09T14:17:00Z"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}

	h = NewHook(HookConfig{URL: srv.URL + "/denied", Root: t.TempDir(), State: state})
	err := h.SendNow(Note{Event: EventTest, Title: "infra · test"})
	if err == nil || !strings.HasPrefix(err.Error(), "notify_url: ") || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("a 403: %v", err)
	}
	if s, _ := LastSent(state); s == nil || !strings.Contains(s.Error, "forbidden") {
		t.Errorf("recorded %+v", s)
	}
	// notify_command wins over notify_url, which it reads as $SWIM_NOTIFY_URL.
	root := t.TempDir()
	h = NewHook(HookConfig{Command: `printf %s "$SWIM_NOTIFY_URL" > url`, URL: "https://ntfy.example/t", Root: root})
	if err := h.SendNow(Note{Event: EventTest}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "url")); string(b) != "https://ntfy.example/t" {
		t.Errorf("SWIM_NOTIFY_URL = %q", b)
	}
	if s, err := LastSent(filepath.Join(t.TempDir(), "none.json")); s != nil || err != nil {
		t.Errorf("nothing sent: %+v, %v", s, err)
	}
}

func TestHookFails(t *testing.T) {
	var mu sync.Mutex
	var warned []string
	h := NewHook(HookConfig{Command: "echo no route to ntfy >&2; exit 6", Root: t.TempDir(), Warn: func(why string) {
		mu.Lock()
		defer mu.Unlock()
		warned = append(warned, why)
	}})
	h.Send(Note{Event: EventAsk})
	h.Wait()
	if len(warned) != 1 || !strings.HasPrefix(warned[0], "notify_command: exit status 6") || !strings.Contains(warned[0], "no route to ntfy") {
		t.Errorf("warned %q", warned)
	}
}

// Each title starts with where it came from: the repo, and the branch when
// it isn't the default.
func TestOriginTitle(t *testing.T) {
	for _, c := range []struct {
		o    Origin
		want string
	}{
		{Origin{}, "lane 2 needs you"},
		{Origin{Repo: "swimlane"}, "swimlane · lane 2 needs you"},
		{Origin{Repo: "swimlane", Branch: "main"}, "swimlane · lane 2 needs you"},
		{Origin{Repo: "swimlane", Branch: "master"}, "swimlane · lane 2 needs you"},
		{Origin{Repo: "swimlane", Branch: "trunk", Default: "trunk"}, "swimlane · lane 2 needs you"},
		{Origin{Repo: "swimlane", Branch: "fix-auth", Default: "main"}, "swimlane@fix-auth · lane 2 needs you"},
	} {
		if got := c.o.Title("lane 2 needs you"); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.o, got, c.want)
		}
	}
	n := Origin{Repo: "ops", Branch: "main"}.Note(Note{Event: EventAsk})
	if n.Repo != "ops" || n.Branch != "main" || n.Event != EventAsk {
		t.Errorf("Note: %+v", n)
	}
}
