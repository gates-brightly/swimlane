package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestHubLink(t *testing.T) {
	set := func(url string) {
		hubErrMu.Lock()
		hubURL, hubReported, hubErrLast, hubQuiet = url, false, "", true
		hubErrMu.Unlock()
	}
	t.Cleanup(func() { set(""); quietHub(false) })

	set("")
	if link, _ := hubLink(); link != "" {
		t.Fatalf("not enrolled: got %q, want nothing", link)
	}
	set("http://hub:7700")
	if link, _ := hubLink(); !strings.Contains(link, "connecting") {
		t.Fatalf("before a report: got %q", link)
	}
	reportHubError(nil)
	if link, _ := hubLink(); !strings.HasPrefix(link, "(hub online) http://hub:7700") {
		t.Fatalf("after a good report: got %q", link)
	}
	reportHubError(errors.New("connection refused"))
	if link, _ := hubLink(); !strings.HasPrefix(link, "(offline mode)") {
		t.Fatalf("after a failed report: got %q", link)
	}
	reportHubError(nil)
	if link, _ := hubLink(); !strings.HasPrefix(link, "(hub online)") {
		t.Fatalf("back online: got %q", link)
	}
}

// The first report failing is offline mode, not "connecting" forever.
func TestHubLinkOfflineFromStart(t *testing.T) {
	hubErrMu.Lock()
	hubURL, hubReported, hubErrLast, hubQuiet = "http://hub:7700", false, "", true
	hubErrMu.Unlock()
	t.Cleanup(func() {
		hubErrMu.Lock()
		hubURL, hubReported, hubErrLast, hubQuiet = "", false, "", false
		hubErrMu.Unlock()
	})
	reportHubError(errors.New("no such host"))
	if link, _ := hubLink(); !strings.HasPrefix(link, "(offline mode)") {
		t.Fatalf("got %q", link)
	}
}
