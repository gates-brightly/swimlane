package e2e

import (
	"net"
	"strings"
	"testing"
)

// swim hub on a terminal: a live screen of every worker; Enter opens one,
// Esc comes back, Ctrl-C stops the hub.
func TestHubScreen(t *testing.T) {
	r := newSerialRepo(t, "")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	tr := r.startTUI(nil, "hub", "--addr", addr, "--token", "t0ken", "--admin-token", "adm1n")
	url := "http://" + addr
	s := tr.waitFor("swim hub · " + url)
	contains(t, "empty hub", s, "0 swimmers", "no swimmers yet", "swim auth login "+url)

	r.script(1, "billing cutover", `run "x" true`)
	r.join(t, url, "billing", "read") // its first report: swim watch --once
	s = tr.waitFor("▸ billing")
	contains(t, "swimmer row", s, "1 swimmer ·", "SWIMMER", "── billing · "+r.root)

	// A worker with a key waits for approval: a approves it, here.
	contains(t, "pending", s, "PENDING", "pending: key SHA256:")
	tr.send("a")
	s = tr.waitFor("approved billing (key SHA256:")
	contains(t, "approved", s, "approved ")

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
