package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// buildFor cross-compiles a tiny Go program for goos/goarch.
func buildFor(t *testing.T, goos, goarch string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tiny\ngo 1.21\n"), 0o644)
	out := filepath.Join(dir, "swim")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags=-s -w", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s/%s: %v\n%s", goos, goarch, err, b)
	}
	return out
}

func TestPlatformReadsHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles fixture binaries")
	}
	for _, p := range []string{"linux/amd64", "darwin/arm64", "linux/arm64", "darwin/amd64"} {
		goos, goarch, _ := strings.Cut(p, "/")
		bin := buildFor(t, goos, goarch)
		got, err := Platform(bin)
		if err != nil || len(got) != 1 || got[0] != p {
			t.Errorf("%s: got %v %v", p, got, err)
		}
	}
	script := filepath.Join(t.TempDir(), "swim")
	os.WriteFile(script, []byte("#!/bin/sh\nexec swim \"$@\"\n"), 0o755)
	if _, err := Platform(script); err != ErrScript {
		t.Errorf("script: %v", err)
	}
	junk := filepath.Join(t.TempDir(), "swim")
	os.WriteFile(junk, []byte("not a binary at all"), 0o755)
	if _, err := Platform(junk); err == nil {
		t.Error("junk read as a binary")
	}
}

func TestRuns(t *testing.T) {
	cases := []struct {
		plats        []string
		host         string
		ok, emulated bool
	}{
		{[]string{"linux/amd64"}, "linux/amd64", true, false},
		{[]string{"darwin/arm64"}, "linux/arm64", false, false},
		{[]string{"linux/arm64"}, "linux/amd64", false, false},
		{[]string{"darwin/amd64"}, "darwin/arm64", true, true},
		{[]string{"darwin/amd64", "darwin/arm64"}, "darwin/arm64", true, false},
	}
	for _, c := range cases {
		ok, em := Runs(c.plats, c.host)
		if ok != c.ok || em != c.emulated {
			t.Errorf("%v on %s: %v %v", c.plats, c.host, ok, em)
		}
	}
}

func TestPlatformCheckFlagsWrongBinaryOnPath(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles fixture binaries")
	}
	wrong := buildFor(t, "darwin", "arm64")
	self := buildFor(t, "linux", "amd64")
	fs := Run(Options{Root: t.TempDir(), Cwd: t.TempDir(), Self: self, Host: "linux/amd64", PATH: filepath.Dir(wrong) + ":" + filepath.Dir(self)})
	var platform, multiple, swimBin bool
	for _, f := range fs {
		switch f.Code {
		case "binary-platform":
			platform = f.Level == finding.Error && strings.Contains(f.Message, "built for darwin/arm64, but this host is linux/amd64") && f.File == wrong
		case "path-multiple":
			multiple = strings.Contains(f.Message, wrong+" (runs)") && strings.Contains(f.Message, self+" (hidden)")
		case "swim-bin":
			swimBin = true
		}
	}
	if !platform || !multiple || !swimBin {
		t.Errorf("platform=%v multiple=%v swim-bin=%v: %+v", platform, multiple, swimBin, fs)
	}
}

func TestLookPathAll(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "swim"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(b, "swim"), []byte("#!/bin/sh\n"), 0o644) // not executable
	os.Mkdir(filepath.Join(t.TempDir(), "swim"), 0o755)
	got := LookPathAll("swim", b+":"+a+":"+a)
	if len(got) != 1 || got[0] != filepath.Join(a, "swim") {
		t.Errorf("got %v", got)
	}
}

func opts(root string) Options {
	return Options{
		Root: root, Cwd: root, Cfg: &config.Config{Root: root, HasRepo: true, Settings: config.Settings{Lanes: 4}},
		GitignoreBegin: "# >>> swim >>>", GitignoreEnd: "# <<< swim <<<", GitignoreBody: "lane.[0-9]*.sh\n.swim/\n.swim.log\n",
	}
}

func only(fs []finding.Finding, code string) []finding.Finding {
	var out []finding.Finding
	for _, f := range fs {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func TestGitignoreBlock(t *testing.T) {
	root := t.TempDir()
	o := opts(root)
	o.defaults()
	write := func(s string) { os.WriteFile(filepath.Join(root, ".gitignore"), []byte(s), 0o644) }
	if fs := checkGitignore(&o); len(fs) != 1 || !strings.Contains(fs[0].Message, "no swim block") {
		t.Errorf("missing file: %+v", fs)
	}
	write("node_modules/\n# >>> swim >>>\nlane.[0-9]*.sh\n.swim/\n.swim.log\n# <<< swim <<<\n")
	if fs := checkGitignore(&o); len(fs) != 0 {
		t.Errorf("current block: %+v", fs)
	}
	write("# >>> swim >>>\nlane.[0-9]*.sh\n.swim/\n# <<< swim <<<\n")
	if fs := checkGitignore(&o); len(fs) != 0 {
		t.Errorf(".swim.log dropped on purpose: %+v", fs)
	}
	write("# >>> swim >>>\nnext[0-9]*.sh\n.swim/\n# <<< swim <<<\n")
	fs := checkGitignore(&o)
	if len(fs) != 1 || !strings.Contains(fs[0].Message, "missing lane.[0-9]*.sh") || !strings.Contains(fs[0].Message, "has old next[0-9]*.sh") {
		t.Errorf("stale block: %+v", fs)
	}
}

func TestStalePIDAndFix(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".swim"), 0o755)
	// A pid that can't exist, and our own (alive).
	os.WriteFile(filepath.Join(root, ".swim", "lane2.pid"), []byte("2147483646\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".swim", "lane3.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("keep-me\n"), 0o644)
	o := opts(root)
	o.FixGitignore = func(path string) (bool, error) {
		data, _ := os.ReadFile(path)
		return true, os.WriteFile(path, append(data, []byte("# >>> swim >>>\nlane.[0-9]*.sh\n.swim/\n.swim.log\n# <<< swim <<<\n")...), 0o644)
	}
	fs := Run(o)
	stale := only(fs, "stale-pid")
	if len(stale) != 1 || stale[0].File != filepath.Join(".swim", "lane2.pid") {
		t.Fatalf("stale: %+v", stale)
	}
	done, err := Fix(o, fs)
	if err != nil || len(done) != 2 {
		t.Fatalf("fix: %v %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".swim", "lane2.pid")); !os.IsNotExist(err) {
		t.Error("stale pid file kept")
	}
	if _, err := os.Stat(filepath.Join(root, ".swim", "lane3.pid")); err != nil {
		t.Error("live pid file removed")
	}
	after := Run(o)
	if len(only(after, "stale-pid"))+len(only(after, "gitignore")) != 0 {
		t.Errorf("after fix: %+v", after)
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.HasPrefix(string(gi), "keep-me\n") {
		t.Errorf(".gitignore: %q", gi)
	}
}

func TestHeaderEnvSecretAndToolchain(t *testing.T) {
	root := t.TempDir()
	o := opts(root)
	o.Cfg.HeaderEnv = []string{"STAGE", "DEPLOY_TOKEN", "DB_URL"}
	o.Cfg.SecretEnv = []string{"DB_URL"}
	o.Cfg.Toolchain = "echo loading; exit 3"
	fs := Run(o)
	sec := only(fs, "header-env-secret")
	if len(sec) != 2 || !strings.Contains(sec[0].Message, "DEPLOY_TOKEN") || !strings.Contains(sec[1].Message, "DB_URL") {
		t.Errorf("header_env: %+v", sec)
	}
	tc := only(fs, "toolchain")
	if len(tc) != 1 || tc[0].Level != finding.Error || !strings.Contains(tc[0].Message, "loading") {
		t.Errorf("toolchain: %+v", tc)
	}
	o.Cfg.Toolchain = "true"
	if tc := only(Run(o), "toolchain"); len(tc) != 0 {
		t.Errorf("good toolchain: %+v", tc)
	}
}

func TestLockAndLanesSetting(t *testing.T) {
	root := t.TempDir()
	o := opts(root)
	if l := only(Run(o), "lock"); len(l) != 1 || l[0].Level != finding.Info {
		t.Errorf("no lock: %+v", l)
	}
	os.WriteFile(filepath.Join(root, ".swim.lock"), []byte("breaking: 99\nversion: \"99.1\"\n"), 0o644)
	if l := only(Run(o), "lock"); len(l) != 1 || l[0].Level != finding.Warn || !strings.Contains(l[0].Message, "newer swim") {
		t.Errorf("newer lock: %+v", l)
	}
	if l := only(Run(o), "lanes-setting"); len(l) != 0 {
		t.Errorf("no lanes: setting: %+v", l)
	}
	o.Cfg.RepoLanes = true
	if l := only(Run(o), "lanes-setting"); len(l) != 1 || !strings.Contains(l[0].Message, "no longer used") {
		t.Errorf("lanes: set: %+v", l)
	}
}

// A setting swim no longer has (chime ...) is ignored, and doctor says
// which file still sets it.
func TestRemovedSettings(t *testing.T) {
	o := opts(t.TempDir())
	if l := only(Run(o), "removed-setting"); len(l) != 0 {
		t.Errorf("none set: %+v", l)
	}
	o.Cfg.RepoFile = "swim.yml"
	o.Cfg.Repo.RemovedChime, o.Cfg.Repo.RemovedChimeMinS = true, 30
	if l := only(Run(o), "removed-setting"); len(l) != 1 || l[0].File != "swim.yml" || !strings.Contains(l[0].Message, "chime, chime_min_s: swim no longer has") {
		t.Errorf("swim.yml sets chime: %+v", l)
	}
}

func TestHeadlessChecks(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	plain, yes, plist := filepath.Join(dir, "a.service"), filepath.Join(dir, "b.service"), filepath.Join(dir, "c.plist")
	os.WriteFile(plain, []byte("# -y: in a comment\nExecStart=\"/bin/swim\" \"watch\" \"--headless\"\n"), 0o644)
	os.WriteFile(yes, []byte("ExecStart=\"/bin/swim\" \"watch\" \"--headless\" \"-y\"\n"), 0o644)
	os.WriteFile(plist, []byte("<array>\n\t\t<string>/bin/swim</string>\n\t\t<string>--yes</string>\n</array>\n"), 0o644)
	cfg := &config.Config{Root: root, Settings: config.Settings{Lanes: 1}}
	cfg.Headless = &config.Headless{Approvals: config.HeadlessHub}
	o := Options{Root: root, Cfg: cfg, Headless: &Headless{Units: [][2]string{{"systemd", plain}, {"systemd", yes}, {"launchd", plist}}, Stale: 4242,
		HubCheck: func() error { return os.ErrDeadlineExceeded }}}
	o.defaults()
	codes := map[string]int{}
	var text []string
	for _, c := range []func(*Options) []finding.Finding{checkHeadlessService, checkHeadlessStale, checkHubReachable, checkHeadlessHubAnswers, checkHeadlessYes} {
		for _, f := range c(&o) {
			codes[f.Code]++
			text = append(text, f.Message)
		}
	}
	want := map[string]int{"headless-service": 3, "headless-stale": 1, "hub-unreachable": 1, "headless-hub-answers": 1, "headless-yes": 2}
	for k, n := range want {
		if codes[k] != n {
			t.Errorf("%s: %d findings, want %d:\n%s", k, codes[k], n, strings.Join(text, "\n"))
		}
	}
	// Nothing set up: nothing said (and the hub isn't tried).
	tried := false
	o = Options{Root: root, Headless: &Headless{HubCheck: func() error { tried = true; return nil }}}
	o.defaults()
	for _, c := range []func(*Options) []finding.Finding{checkHeadlessService, checkHeadlessStale, checkHubReachable, checkHeadlessHubAnswers, checkHeadlessYes} {
		if fs := c(&o); len(fs) > 0 {
			t.Errorf("idle repo: %+v", fs)
		}
	}
	if tried {
		t.Error("the hub was reached with no headless worker here")
	}
}

// A runner key in a plain file is a warning: a lane could read it. One a
// lane can only reach through ssh-agent is a note.
func TestSigningKey(t *testing.T) {
	if !sign.Available() {
		t.Skip("ssh-keygen not installed")
	}
	p := filepath.Join(t.TempDir(), "runner")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	o := &Options{Root: t.TempDir(), Cfg: &config.Config{}}
	o.Cfg.SigningKey = p
	if fs := checkSigningKey(o); len(fs) != 1 || fs[0].Level != finding.Warn || !strings.Contains(fs[0].Message, "no passphrase") {
		t.Errorf("a plain file: %+v", fs)
	}
	os.Remove(p)
	o.Cfg.SigningKey = p + ".pub"
	if fs := checkSigningKey(o); len(fs) != 1 || fs[0].Level != finding.Info || !strings.Contains(fs[0].Message, "ssh-agent") {
		t.Errorf("in the agent only: %+v", fs)
	}
	o.Cfg.Signing = config.SigningOff
	if fs := checkSigningKey(o); len(fs) != 0 {
		t.Errorf("signing off: %+v", fs)
	}
}
