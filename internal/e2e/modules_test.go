package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// modScript is r.script for a lane that uses modules: syntax 4.
func (r *repo) modScript(n int, round, body string) {
	r.t.Helper()
	r.script(n, round, body)
	p := filepath.Join(r.root, fmt.Sprintf("lane.%d.sh", n))
	data, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(data), "# swim: syntax 2", "# swim: syntax 4", 1)), 0o755)
}

// gitRepo makes a repository with one commit and returns its path and the
// commit id.
func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README"), []byte("v1\n"), 0o644)
	git("add", "README")
	git("commit", "-q", "-m", "one")
	sha := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "README"), []byte("v2\n"), 0o644)
	git("commit", "-q", "-am", "two")
	return dir, sha
}

// The clone module checks out exactly the commit it's given (not main's
// tip) and refuses a branch name; a module the header doesn't declare is
// refused at the step and by lint; swim plan names the modules.
func TestModuleClone(t *testing.T) {
	r := newRepo(t, "")
	src, sha := gitRepo(t)
	r.modScript(1, "clone at a commit", `# Modules: clone
mod clone "`+src+`" `+sha+` "$SWIM_OUT/src"
run "read it" cat "$SWIM_OUT/src/README"
mod --label "clone a branch" clone "`+src+`" main "$SWIM_OUT/branch"
mod notify - "not declared"`)
	out, _ := r.swim("lint", "1")
	contains(t, "lint", out, "module notify isn't declared")
	contains(t, "plan", r.mustSwim("plan", "1"), "modules", "clone 1.0.0 (built in")
	if out, code := r.swim("run", "1", "--plain"); code == 0 || !strings.Contains(out, "module notify isn't declared") {
		t.Errorf("swim run of a lane with an undeclared module: %d\n%s", code, out)
	}
	r.cmd("bash", "lane.1.sh").Run() // run directly, the step itself is refused
	log := r.log(1)
	contains(t, "log", log, "PASS  clone "+src+" "+sha, "clone: HEAD is "+sha, "| v1", "FAIL  clone a branch",
		"isn't a full commit id", "FAIL  notify - not declared", "module notify isn't declared in lane.1.sh: add `# Modules: notify`")
}

// notify posts to the webhook in the secret, never in its arguments; when
// the webhook is down it fails soft, unless --strict.
func TestModuleNotify(t *testing.T) {
	r := newRepo(t, "")
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	r.env = append(r.env, "SWIM_SECRET_SLACK_WEBHOOK_URL="+srv.URL+"/hook/secret-path")
	r.modScript(1, "tell the channel", `# Modules: notify
mod notify "#orders" "deployed \"orders\" from C:\\deploy in $SWIM_JOB"`)
	r.mustSwim("run", "1", "--plain")
	mu.Lock()
	got := strings.Join(bodies, "\n")
	mu.Unlock()
	contains(t, "posted", got, `"channel": "#orders"`, `"text": "deployed \"orders\" from C:\\deploy in `)
	log := r.log(1)
	contains(t, "log", log, "PASS  notify #orders", "notify: sent to #orders")
	if strings.Contains(log, "secret-path") {
		t.Errorf("the webhook URL reached the log:\n%s", log)
	}

	srv.Close()
	r.modScript(2, "tell it again", `# Modules: notify
mod notify - "soft"
mod --label strict notify --strict - "strict"`)
	r.swim("run", "2", "--plain")
	contains(t, "down", r.log(2), "PASS  notify - soft", "notify: not sent: the webhook didn't take it (soft", "FAIL  strict")
}

// Repo modules: allowed by swim.yml, pinned and staged with the lane, run
// from the stage; an edit stales a queued batch; mod_env gives the lane
// variables, masked, and refuses a credential from a module that doesn't
// say it makes them.
func TestModuleRepoAndEnv(t *testing.T) {
	r := newRepo(t, "")
	mk := func(name, spec, src string) {
		d := filepath.Join(r.root, "modules", name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "module.yml"), []byte(spec), 0o644)
		os.WriteFile(filepath.Join(d, name+".sh"), []byte(src), 0o644)
	}
	mk("greet", "schema: swim.module/v1\nname: greet\nversion: 1.0.0\nkind: bash\ncommands: [greet, greet_env, greet_bad]\n",
		`greet() { echo "hello $1 from greet-v1"; }
greet_env() { echo "GREET_TARGET=world" >>"$SWIM_MODULE_OUT"; echo "GREET_LONG=value-to-mask-123" >>"$SWIM_MODULE_OUT"; }
greet_bad() { echo "AWS_SECRET_ACCESS_KEY=abcdefabcdef" >>"$SWIM_MODULE_OUT"; }
`)
	r.modScript(1, "use a repo module", `# Modules: greet
mod greet there
mod_env greet_env
run "see it" bash -c 'echo "target=$GREET_TARGET long=$GREET_LONG"'
mod_env greet_bad
run "not set" bash -c 'echo "aws=${AWS_SECRET_ACCESS_KEY:-unset}"'`)

	// Found by name with swim.yml's modules: dir, and allowed by it.
	out, _ := r.swim("run", "1", "--plain")
	contains(t, "no dir", out, "./modules/greet is one: declare it as ./modules/greet")
	r.write("swim.yml", "modules:\n  dir: modules\n", 0o644)
	out, _ = r.swim("run", "1", "--plain")
	contains(t, "not allowed", out, "add ./modules/greet to swim.yml modules: allow:")
	r.write("swim.yml", "modules:\n  allow: [./modules/*]\n  dir: modules\n", 0o644)
	contains(t, "modules ls", r.mustSwim("modules"), "greet", "./modules/greet", "clone", "not allowed by swim.yml")
	contains(t, "lint", must(r.swim("lint", "1")), "")

	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "PASS  greet there", "hello there from greet-v1", "PASS  greet_env", "gave the lane GREET_TARGET GREET_LONG",
		"PASS  see it", "target=world long=***", "FAIL  greet_bad", "AWS_SECRET_ACCESS_KEY, a credential, but its module.yml doesn't say credentials: true",
		"aws=unset")
	if strings.Contains(log, "value-to-mask-123") {
		t.Errorf("a mod_env value reached the log:\n%s", log)
	}
	run := r.status().Get(1).Run
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "runs", run, "1", "modules", "greet", "greet.sh")); err != nil {
		t.Errorf("the module isn't staged: %v", err)
	}

	// A queued batch pins the module: an edit since skips it.
	r.mustSwim("queue", "add", "1")
	os.WriteFile(filepath.Join(r.root, "modules", "greet", "greet.sh"), []byte(`greet() { echo "hello $1 from greet-v2"; }
greet_env() { :; }
greet_bad() { :; }
`), 0o644)
	r.swim("watch", "--once")
	if st := r.status().Get(1); st.State != "skipped" || !strings.Contains(st.Reason, "its manifest changed since it was queued") {
		t.Errorf("swim 1 after its module changed: %s %q", st.State, st.Reason)
	}
}

// aws-session (a built-in Go module) asks the AWS CLI for credentials and
// gives them to the lane's later steps, masked; under plain mod it refuses.
func TestModuleAWSSession(t *testing.T) {
	r := newRepo(t, "")
	fake := t.TempDir()
	os.WriteFile(filepath.Join(fake, "aws"), []byte(`#!/usr/bin/env bash
echo "$@" >>"$AWS_FAKE_LOG"
case "$1 $2" in
  "sts assume-role") echo '{"Credentials": {"AccessKeyId": "ASIAFAKEFAKEFAKE1234", "SecretAccessKey": "fake-secret-key-abcdef", "SessionToken": "fake-session-token-xyz", "Expiration": "2026-10-16T12:00:00Z"}}' ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`), 0o755)
	r.env = append(r.env, "PATH="+fake+string(os.PathListSeparator)+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AWS_FAKE_LOG="+filepath.Join(fake, "calls"))
	r.modScript(1, "with a session", `# Modules: aws-session
mod aws-session --profile p
mod_env aws-session --profile orders --role arn:aws:iam::123456789012:role/deploy --duration 30m
run "use it" bash -c 'echo "key=$AWS_ACCESS_KEY_ID secret=$AWS_SECRET_ACCESS_KEY until=$AWS_CREDENTIAL_EXPIRATION"'`)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "FAIL  aws-session --profile p", "call it with mod_env, not mod",
		"PASS  aws-session --profile orders", "assuming arn:aws:iam::123456789012:role/deploy", "key=*** secret=*** until=***")
	if strings.Contains(log, "fake-secret-key") || strings.Contains(log, "fake-session-token") {
		t.Errorf("credentials reached the log:\n%s", log)
	}
	calls, _ := os.ReadFile(filepath.Join(fake, "calls"))
	contains(t, "aws calls", string(calls), "--role-session-name swim-", "--duration-seconds 1800")
}

// A repo Go module: swim modules new writes one, swim builds it (once per
// source) with the operator's Go and runs it from the stage; under mod_env
// it gives the lane variables like a bash module.
func TestModuleRepoGo(t *testing.T) {
	r := newRepo(t, "")
	r.write("swim.yml", "modules:\n  allow: [./modules/*]\n  dir: modules\n", 0o644)
	contains(t, "new", r.mustSwim("modules", "new", "stamp", "--kind", "go"), "created modules/stamp/ (go module)")
	r.write("modules/stamp/main.go", `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("stamp:", os.Args[1:], "module", os.Getenv("SWIM_MODULE"), os.Getenv("SWIM_MODULE_VERSION"))
	if out := os.Getenv("SWIM_MODULE_OUT"); out != "" {
		os.WriteFile(out, []byte("STAMP=stamped-value-42\n"), 0o600)
	}
}
`, 0o644)
	r.modScript(1, "go module", `# Modules: stamp
mod stamp a b
mod_env stamp
run "see" bash -c 'echo "stamp=$STAMP"'`)
	contains(t, "lint", must(r.swim("lint", "1")), "")
	// Built once, as the lane is staged (not at its first call), and the
	// binary recorded in the manifest.
	out1, _ := r.swim("run", "1", "--plain")
	out2, _ := r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "PASS  stamp a b", "stamp: [a b] module stamp 0.1.0", "PASS  see", "stamp=***")
	if n := strings.Count(out1+out2, "stamp: building ./modules/stamp"); n != 1 {
		t.Errorf("built %d times, want once:\n%s\n%s", n, out1, out2)
	}
	mans, _ := filepath.Glob(filepath.Join(r.root, ".swim", "runs", "*", "1", "manifest.yml"))
	if len(mans) == 0 {
		t.Fatal("no staged manifest")
	}
	contains(t, "manifest", readFile(t, r, strings.TrimPrefix(mans[0], r.root+"/")), "built:", "module: stamp", "go: go1.", "path: .swim/modules/build/")
	contains(t, "show", r.mustSwim("modules", "show", "stamp"), "# stamp 0.1.0, ./modules/stamp", "# --- main.go")
}

// A signed approval covers the lane's modules: swim approve shows them,
// and an edit to a repo module stales it, with the reason.
func TestModuleApprovalStale(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "require:\n  - guards: [MOD_ALLOW_*]\n    count: 1\n", "zach")
	r.write("swim.yml", "modules:\n  allow: [notify, ./modules/*]\n", 0o644)
	d := filepath.Join(r.root, "modules", "say")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "module.yml"), []byte("schema: swim.module/v1\nname: say\nversion: 1.0.0\nkind: bash\ncommands: [say]\n"), 0o644)
	os.WriteFile(filepath.Join(d, "say.sh"), []byte("say() { echo \"say-v1 $*\"; }\n"), 0o644)
	r.modScript(1, "say it", `if guard MOD_ALLOW_SAY "say it"; then mod say hello; fi`)
	withHeader(t, r, 1, "# Modules: ./modules/say notify\n# Guards: MOD_ALLOW_SAY say it")
	contains(t, "approve", r.mustSwim("approve", "1", "--guards", "MOD_ALLOW_SAY", "--key", keys["zach"]),
		"includes modules/say/say.sh", "(module)", "module   say 1.0.0 ./modules/say", "module   notify 1.0.0 builtin", "secret SLACK_WEBHOOK_URL")
	r.mustSwim("run", "1")
	contains(t, "approved", r.log(1), "APPROVED  say it (approved by ", "say-v1 hello")

	os.WriteFile(filepath.Join(d, "say.sh"), []byte("say() { echo \"say-v2 $*\"; }\n"), 0o644)
	r.mustSwim("run", "1")
	contains(t, "stale", r.log(1), "stale: includes modules/say/say.sh changed; module say changed")
}

// target, backup and wait in a round: target passes for the right account
// and stops the round for a wrong one (a gate module, even under plain
// mod); backup's id reaches the same round (mod_env) and a later lane
// (input); wait polls until the service answers.
func TestModuleTargetBackupWait(t *testing.T) {
	r := newRepo(t, "")
	fake := t.TempDir()
	os.WriteFile(filepath.Join(fake, "aws"), []byte(`#!/usr/bin/env bash
n=$(cat "$AWS_FAKE_DIR/n" 2>/dev/null || echo 0); n=$((n+1)); echo $n >"$AWS_FAKE_DIR/n"
case "$1 $2" in
  "sts get-caller-identity") echo '{"Account": "123456789012", "Arn": "arn:aws:iam::123456789012:user/ops"}' ;;
  "rds create-db-snapshot") echo '{}' ;;
  "rds describe-db-snapshots") st=creating; [ "$n" -ge 4 ] && st=available; echo "{\"DBSnapshots\": [{\"Status\": \"$st\"}]}" ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`), 0o755)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(502)
		}
	}))
	defer srv.Close()
	r.env = append(r.env, "PATH="+fake+string(os.PathListSeparator)+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AWS_FAKE_DIR="+fake)
	r.modScript(1, "back up, change, wait", `# Modules: target backup wait
# Outputs: -
mod target aws --account 123456789012
stage change
mod_env backup rds orders-prod --every 50ms
run "same round" bash -c 'echo "kind=$BACKUP_KIND source=$BACKUP_SOURCE id-set=${BACKUP_ID:+yes}"'
mod wait http "`+srv.URL+`/health" --every 50ms --for 20s`)
	r.script(2, "reads the backup id", `run "restore point" echo "from $(input 1 backup.rds.orders-prod)"`)
	withHeader(t, r, 2, "# After: 1")
	r.modScript(3, "wrong account", `# Modules: target
mod target aws --account 999999999999
run "never" echo should-not-run`)
	contains(t, "lint", must(r.swim("lint", "1", "2", "3")), "")

	r.swim("run", "1", "2", "--plain")
	log1, log2 := r.log(1), r.log(2)
	contains(t, "lane 1", log1, "PASS  target aws --account 123456789012", "AWS account 123456789012, as the lane says",
		"PASS  backup rds orders-prod", "backup: available", "recorded as output backup.rds.orders-prod",
		"kind=rds source=*** id-set=yes", "PASS  wait http", "wait: ready after")
	contains(t, "lane 2", log2, "PASS  restore point", "from swim-1-")

	r.swim("run", "3", "--plain")
	log3 := r.log(3)
	contains(t, "lane 3", log3, "FAIL  target aws --account 999999999999", "AWS account is 123456789012, not 999999999999", "STOP  gate failed: target aws")
	if strings.Contains(log3, "should-not-run") {
		t.Errorf("the round went on after a wrong target:\n%s", log3)
	}
}

// fakeBin puts a fake tool first on the repo's PATH.
func fakeBin(t *testing.T, r *repo, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, name), []byte("#!/usr/bin/env bash\n"+script), 0o755)
	r.env = append(r.env, "PATH="+dir+string(os.PathListSeparator)+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// tf: the plan is the round's evidence, so a signed approval covers it;
// apply runs only the saved plan, only when the guard is approved, and not
// once the plan changed.
func TestModuleTF(t *testing.T) {
	r := newRepo(t, "")
	keys := people(t, r, "", "alice")
	os.MkdirAll(filepath.Join(r.root, "infra"), 0o755)
	r.write("infra/main.tf", "# fake\n", 0o644)
	r.write("plan.txt", "# aws_dynamodb_table.orders will be destroyed\n", 0o644)
	fakeBin(t, r, "terraform", `case "$2" in
  init) echo "init noise $RANDOM" ;;
  plan) for a in "$@"; do case "$a" in -out=*) cat "$SWIM_ROOT/plan.txt" >"${a#-out=}" ;; esac; done; echo "refresh noise $RANDOM"; exit 2 ;;
  show) cat "$4" ;;
  apply) echo "Apply complete! $(head -1 "${@: -1}")"; touch "$SWIM_ROOT/applied" ;;
esac
`)
	r.modScript(1, "apply the orders plan", `# Modules: tf
# Guards: ORDERS_ALLOW_APPLY  apply the orders plan
stage check
mod --evidence tf plan infra
stage change
if guard ORDERS_ALLOW_APPLY "apply the orders plan"; then mod tf apply infra; fi`)
	contains(t, "lint", must(r.swim("lint", "1")), "no problems found")
	contains(t, "plan", r.mustSwim("plan", "1"), "tf 1.0.0 (built in: ", "read-only for plan, guard apply")

	r.swim("plan", "--check", "1")
	contains(t, "approve", r.mustSwim("approve", "1", "--guards", "ORDERS_ALLOW_APPLY", "--key", keys["alice"]),
		"evidence from swim plan --check", "aws_dynamodb_table.orders will be destroyed")
	if _, err := os.Stat(filepath.Join(r.root, "applied")); err == nil {
		t.Fatal("swim plan --check applied")
	}
	r.mustSwim("run", "1", "--plain", "--no-prompt")
	log := r.log(1)
	contains(t, "approved", log, "PASS  evidence: tf plan infra", "APPROVED  apply the orders plan (approved by alice", "PASS  tf apply infra",
		"applying the plan of infra saved this round", "Apply complete! # aws_dynamodb_table.orders will be destroyed")
	if strings.Contains(log, "noise") {
		t.Errorf("init or plan chatter reached the evidence step:\n%s", log)
	}

	// The plan changed: the approval doesn't count, and nothing is applied.
	os.Remove(filepath.Join(r.root, "applied"))
	r.write("plan.txt", "# aws_dynamodb_table.orders will be destroyed\n# aws_s3_bucket.orders will be destroyed\n", 0o644)
	r.swim("run", "1", "--plain", "--no-prompt")
	last := r.log(1)[strings.LastIndex(r.log(1), "== ROUND"):]
	contains(t, "changed plan", last, "WARN  evidence changed since approval", "SKIP  apply the orders plan (dry run")
	if _, err := os.Stat(filepath.Join(r.root, "applied")); err == nil {
		t.Error("applied a plan nobody approved")
	}
}

// smoke runs the pinned checks file once; an unpinned file is refused.
func TestModuleSmoke(t *testing.T) {
	r := newRepo(t, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/health" {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	r.env = append(r.env, "ORDERS_URL="+srv.URL)
	os.MkdirAll(filepath.Join(r.root, "smoke"), 0o755)
	r.write("smoke/orders.yml", "base: ${ORDERS_URL}\nchecks:\n  - get: /health\n  - name: api\n    get: /api\n", 0o644)
	r.modScript(1, "smoke it", `# Modules: smoke
# Includes: smoke/orders.yml
mod smoke smoke/orders.yml`)
	r.modScript(2, "smoke without pinning", `# Modules: smoke
mod smoke smoke/orders.yml`)
	r.swim("run", "1", "2", "--plain")
	contains(t, "lane 1", r.log(1), "FAIL  smoke smoke/orders.yml", "ok   get /health: HTTP 200", "FAIL api: HTTP 500, want 2xx", "1 of 2 checks failed")
	contains(t, "lane 2", r.log(2), "add `# Includes: smoke/orders.yml`")
}

// claude writes an artifact with the CLI swim is told to use, from the
// pinned prompt and the round's masked log; it never decides anything.
func TestModuleClaude(t *testing.T) {
	r := newRepo(t, "")
	dir := t.TempDir()
	cli := filepath.Join(dir, "my-claude")
	os.WriteFile(cli, []byte(`#!/usr/bin/env bash
[ "$1" = --version ] && { echo "1.2.3 (Claude Code)"; exit 0; }
cat >"`+dir+`/stdin"
echo "# Run report"
echo "All steps passed."
`), 0o755)
	r.env = append(r.env, "MY_CLI="+cli, "DEPLOY_TOKEN=tok-abcdef-123456")
	os.MkdirAll(filepath.Join(r.root, "prompts"), 0o755)
	r.write("prompts/report.md", "Write a short run report.\n", 0o644)
	r.modScript(1, "report", `# Modules: claude report
# Includes: prompts/report.md
# Outputs: report.md
run "uses a token" bash -c 'echo "token=$DEPLOY_TOKEN"'
mod claude --prompt-file prompts/report.md --log --out report.md -- "$MY_CLI"
mod report report.md
mod report --optional missing.md`)
	contains(t, "lint", must(r.swim("lint", "1")), "no problems found")
	r.mustSwim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "PASS  claude --prompt-file", "claude: "+cli+" (1.2.3 (Claude Code), sha256 ", "wrote report.md",
		"PASS  report report.md", "| == report.md  ", "| # Run report", "| All steps passed.", "PASS  report --optional missing.md", "== missing.md: not there")
	st := r.status().Get(1)
	report, err := os.ReadFile(filepath.Join(r.root, ".swim", "out", st.Job, st.Run, "report.md"))
	if err != nil || !strings.Contains(string(report), "All steps passed.") {
		t.Errorf("report: %q %v", report, err)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	contains(t, "sent", string(stdin), "Write a short run report.", "PASS  uses a token", "token=***")
	if strings.Contains(string(stdin), "tok-abcdef") {
		t.Errorf("an unmasked secret was sent:\n%s", stdin)
	}
}

// A command that fails outside any step (round 1 of the claude test: mod
// wasn't defined by an older swim) leaves a WARN in the log; steps,
// conditions and && / || lists don't.
func TestFailureOutsideStepWarns(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "outside steps", `nosuch_command_xyz --flag
cd /nonexistent/dir
run "a failing step" false
if false; then echo never; fi
false || true
[ -f /nope ] && echo hi
run "after" echo "still runs"`)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "WARN  outside a step: nosuch_command_xyz --flag (exit 127: command not found",
		"WARN  outside a step: cd /nonexistent/dir (exit 1", "FAIL  a failing step", "PASS  after", "| still runs")
	if n := strings.Count(log, "WARN  outside a step"); n != 2 {
		t.Errorf("%d outside-a-step WARNs, want 2:\n%s", n, log)
	}
}

// A lane program with no SDK calls modules with swim mod: a step, as a
// bash lane's mod runs it, and a failed gate module ends the round.
func TestModuleFromAPlainProgram(t *testing.T) {
	r := newRepo(t, "")
	fakeBin(t, r, "aws", `echo '{"Account": "123456789012"}'`+"\n")
	r.write("lane.1.sh", `#!/usr/bin/env bash
# swim: syntax 4
# Round: modules with no SDK
# Modules: target
swim mod target aws --account 123456789012
swim mod --label "right account, labelled" target aws --account 123456789012
swim mod target aws --account 999999999999
swim run "never" -- echo should-not-run
`, 0o755)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log, "PASS  target aws --account 123456789012", "PASS  right account, labelled",
		"FAIL  target aws --account 999999999999", "STOP  gate failed: target aws --account 999999999999")
	if strings.Contains(log, "should-not-run") {
		t.Errorf("the round went on after a failed gate module:\n%s", log)
	}
	if out, code := r.swim("mod", "target", "aws"); code == 0 || !strings.Contains(out, "runs inside a lane's round") {
		t.Errorf("swim mod outside a round: %d %s", code, out)
	}
}
