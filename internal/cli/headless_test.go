package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(path, []byte(got), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// A path with a space, a percent sign, a quote and a dollar exercises the
// unit's quoting; & and < the plist's escaping.
func testUnit(yes bool) headlessUnit {
	root := "/home/al/src/my repo %1 \"q\" $X & <b>"
	args := []string{"/opt/swim bin/swim", "watch", "--headless", "--interval", "5s"}
	if yes {
		args = append(args, "-y")
	}
	return headlessUnit{ID: "my-repo-1-q-x-b-0a1b2c3d", Label: "com.swim.my-repo-1-q-x-b-0a1b2c3d", Root: root,
		Args: args, Path: "/opt/swim bin:/usr/bin:/bin", User: "al", Log: "/Users/al/Library/Logs/swim/my-repo-1-q-x-b-0a1b2c3d.log",
		StopTimeout: 40, Yes: yes}
}

func TestUnitGolden(t *testing.T) {
	for _, c := range []struct {
		kind, file string
		yes        bool
	}{
		{"systemd", "headless.service", false},
		{"systemd", "headless-yes.service", true},
		{"launchd", "headless.plist", false},
	} {
		got, err := renderUnit(c.kind, testUnit(c.yes))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, c.file, got)
	}
}

func TestHeadlessID(t *testing.T) {
	a, b := headlessID("/srv/Billing Service"), headlessID("/srv/other/Billing Service")
	if !strings.HasPrefix(a, "billing-service-") || len(a) != len("billing-service-")+8 {
		t.Errorf("id %q", a)
	}
	if a == b {
		t.Errorf("two repos of one name share an id: %q", a)
	}
	if a != headlessID("/srv/Billing Service") {
		t.Error("the id isn't stable")
	}
	if id := headlessID("/"); !strings.HasPrefix(id, "repo-") {
		t.Errorf("root dir id %q", id)
	}
}

// The log rotates past max (keeping keep old ones) and reopens onto the
// descriptors writing to it.
func TestHeadlessLogRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "headless.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l := &headlessLog{path: path, max: 10, keep: 2, fds: []int{int(f.Fd())}}
	for i := 0; i < 4; i++ {
		f.WriteString("0123456789-" + string(rune('a'+i)) + "\n")
		if err := l.rotateIfBig(); err != nil {
			t.Fatal(err)
		}
	}
	f.WriteString("tail\n")
	read := func(p string) string { data, _ := os.ReadFile(p); return string(data) }
	if got := read(path); got != "tail\n" {
		t.Errorf("current log %q", got)
	}
	if got := read(path + ".1"); !strings.HasSuffix(got, "-d\n") {
		t.Errorf(".1 %q", got)
	}
	if got := read(path + ".2"); !strings.HasSuffix(got, "-c\n") {
		t.Errorf(".2 %q", got)
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("kept more than keep")
	}
	// logrotate moved it away: reopen makes a new one.
	os.Rename(path, path+".moved")
	if err := l.reopen(); err != nil {
		t.Fatal(err)
	}
	f.WriteString("after\n")
	if got := read(path); got != "after\n" {
		t.Errorf("after reopen %q", got)
	}
}
