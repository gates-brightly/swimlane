package history

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	ts := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	got := Format(ts, "zach", Entry{Event: New, Lane: 1, Job: "3f2a9c1e", Detail: "Cut over\norders-api"})
	want := "2026-10-09T12:00:00Z  zach      new          swim 1  job=3f2a9c1e  Cut over orders-api"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := Format(ts, "zach", Entry{Event: Init}); strings.HasSuffix(got, " ") {
		t.Fatalf("trailing space: %q", got)
	}
}

func TestAppendHeaderOnceAndConcurrentLines(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Append(root, Entry{Event: Note, Detail: fmt.Sprintf("note %d %s", i, strings.Repeat("x", 200))}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	data, _ := os.ReadFile(Path(root))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var events int
	for _, l := range lines {
		if strings.HasPrefix(l, "#") {
			continue
		}
		events++
		if !strings.Contains(l, "  note  ") || !strings.HasSuffix(l, strings.Repeat("x", 200)) {
			t.Errorf("mangled line: %q", l)
		}
	}
	if events != 50 {
		t.Fatalf("events = %d", events)
	}
	if n := strings.Count(string(data), "# swim project log"); n != 1 || !strings.HasPrefix(string(data), "# swim project log") {
		t.Fatalf("header written %d times / not first", n)
	}
}
