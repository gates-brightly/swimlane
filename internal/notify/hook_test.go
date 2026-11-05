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
)

func TestHook(t *testing.T) {
	root := t.TempDir()
	h := NewHook(HookConfig{Command: `printf '%s|%s|%s|%s|%s|%s|%s|%s|%s' "$SWIM_EVENT" "$SWIM_RESULT" "$SWIM_TITLE" "$SWIM_BODY" "$SWIM_LANES" "$SWIM_BATCH" "$PWD" "$SWIM_PRIORITY" "$SWIM_TAGS" > note`,
		Root: root, Warn: func(why string) { t.Error(why) }})
	h.Send(Note{Event: EventEnd, Result: "failed", Title: "swim: #4 failed", Body: "swim 1,2\n· 3s\x07", Lanes: []int{1, 2}, Batch: 4})
	h.Wait()
	b, err := os.ReadFile(filepath.Join(root, "note"))
	if err != nil {
		t.Fatal(err)
	}
	// One line, no controls: a lane's text reaches the command as data.
	if got, want := string(b), "end|failed|swim: #4 failed|swim 1,2 · 3s|1,2|4|"+root+"|high|x"; got != want {
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
	if err := h.SendNow(Note{Event: EventAsk, Title: "swim 3 needs you", Body: "approval of DEPLOY: deploy 2.4.1", Lanes: []int{3}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	r, b := got, body
	mu.Unlock()
	if r.URL.Path != "/zach" || r.Method != http.MethodPost || b != "approval of DEPLOY: deploy 2.4.1" ||
		r.Header.Get("Title") != "swim 3 needs you" || r.Header.Get("Priority") != "high" || r.Header.Get("Tags") != "question" {
		t.Errorf("sent %s %s %q %v", r.Method, r.URL.Path, b, r.Header)
	}
	if s, err := LastSent(state); err != nil || s == nil || s.Error != "" || s.Title != "swim 3 needs you" || s.Event != EventAsk {
		t.Errorf("recorded %+v, %v", s, err)
	}

	h = NewHook(HookConfig{URL: srv.URL + "/denied", Root: t.TempDir(), State: state})
	err := h.SendNow(Note{Event: EventTest, Title: "swim: test"})
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
