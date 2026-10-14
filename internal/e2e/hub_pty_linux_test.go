package e2e

import (
	"net"
	"strings"
	"testing"
)

// swim hub on a terminal: a live screen of every worker; Enter opens one,
// Esc comes back, Ctrl-C stops the hub.
func TestHubScreen(t *testing.T) {
	r := newRepo(t, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	tr := r.startTUI(nil, "hub", "--addr", addr, "--token", "t0ken")
	url := "http://" + addr
	s := tr.waitFor("swim hub · " + url)
	contains(t, "empty hub", s, "0 workers", "no workers yet", "swim hub enroll "+url+" --token t0ken")

	r.script(1, "billing cutover", `run "x" true`)
	r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "billing")
	s = tr.waitFor("▸ billing")
	contains(t, "worker row", s, "1 worker ·", "WORKER", "enrolled; not watching", "── billing · "+r.root)

	tr.send("\r")
	s = tr.waitFor("esc back")
	contains(t, "worker detail", s, "swim hub · billing", "▸ swim 1", "── swim 1", "billing · ")
	tr.send("\x1b")
	tr.waitFor("enter open")
	tr.send("\x03")
	code, out := tr.wait()
	if code != 0 || !strings.Contains(out, "swim hub stopped") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}
