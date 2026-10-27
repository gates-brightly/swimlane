package e2e

import (
	"bufio"
	"os/exec"
	"regexp"
	"syscall"
	"testing"
)

// startHub runs swim hub (plain output) with state in dir and returns its URL.
func startHub(t *testing.T, r *repo, dir, addr string) (string, func()) {
	t.Helper()
	h := exec.Command(bin, "hub", "--addr", addr, "--token", "t0ken", "--state", dir, "--admin-token", "adm1n", "--no-tui")
	h.Env = r.env
	h.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := h.StdoutPipe()
	if err := h.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(out)
	sc.Scan()
	url := regexp.MustCompile(`http://[0-9.:]+`).FindString(sc.Text())
	if url == "" {
		t.Fatalf("no hub URL in %q", sc.Text())
	}
	go func() {
		for sc.Scan() {
		}
	}()
	return url, func() { syscall.Kill(-h.Process.Pid, syscall.SIGTERM); h.Wait() }
}
