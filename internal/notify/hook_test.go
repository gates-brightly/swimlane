package notify

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHook(t *testing.T) {
	root := t.TempDir()
	h := NewHook(`printf '%s|%s|%s|%s|%s|%s|%s' "$SWIM_EVENT" "$SWIM_RESULT" "$SWIM_TITLE" "$SWIM_BODY" "$SWIM_LANES" "$SWIM_BATCH" "$PWD" > note`, root, func(why string) { t.Error(why) })
	h.Send(Note{Event: EventEnd, Result: "failed", Title: "swim: #4 failed", Body: "swim 1,2\n· 3s\x07", Lanes: []int{1, 2}, Batch: 4})
	h.Wait()
	b, err := os.ReadFile(filepath.Join(root, "note"))
	if err != nil {
		t.Fatal(err)
	}
	// One line, no controls: a lane's text reaches the command as data.
	if got, want := string(b), "end|failed|swim: #4 failed|swim 1,2 · 3s|1,2|4|"+root; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	var none *Hook
	none.Send(Note{}) // a nil Hook runs nothing
	none.Wait()
	if NewHook("  ", root, nil) != nil {
		t.Error("a blank command made a Hook")
	}
}

func TestHookFails(t *testing.T) {
	var mu sync.Mutex
	var warned []string
	h := NewHook("echo no route to ntfy >&2; exit 6", t.TempDir(), func(why string) {
		mu.Lock()
		defer mu.Unlock()
		warned = append(warned, why)
	})
	h.Send(Note{Event: EventAsk})
	h.Wait()
	if len(warned) != 1 || !strings.Contains(warned[0], "exit status 6") || !strings.Contains(warned[0], "no route to ntfy") {
		t.Errorf("warned %q", warned)
	}
}
