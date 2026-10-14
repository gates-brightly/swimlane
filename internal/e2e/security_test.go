package e2e

// The security regression suite (tasks.md T25): the audit's checklist, one
// subtest per case. A case asserts what swim does today. A case for a known
// gap calls gap(t, "Tn"), which skips it with the task that will close it,
// so the skip shows in CI logs; finishing that task means replacing the
// gap call with the real assertions.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/sigfile"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// gap skips a case that documents a known gap, naming the task(s) that
// close it (see tasks.md).
func gap(t *testing.T, tasks string) {
	t.Helper()
	t.Skipf("gap: %s (tasks.md)", tasks)
}

// boundary is the execution-boundary fixture: lane 1's guard sits in
// front of a harmless stand-in for a destructive command (it touches a
// marker file), with signing on and a key in lookup.yml.
type boundary struct {
	*repo
	marker string            // exists once the guarded command ran
	keys   map[string]string // name -> private key path ("zach" signs)
}

const boundaryGuard = "SEC_ALLOW_DROP"

func newBoundaryRepo(t *testing.T) *boundary {
	t.Helper()
	r := newRepo(t, "")
	b := &boundary{repo: r, marker: filepath.Join(t.TempDir(), "dropped")}
	b.keys = people(t, r, "", "zach")
	r.mustSwim("config", "signing", "auto")
	r.mustSwim("config", "signing_key", b.keys["zach"])
	b.lane(1, "")
	return b
}

// lane writes lane n with the guarded command; extra goes after it (a
// change to the script).
func (b *boundary) lane(n int, extra string) {
	b.t.Helper()
	b.script(n, "drop orders", `if guard `+boundaryGuard+` "drop table orders"; then run "drop" touch "`+b.marker+`"; fi
run "after" true`+extra)
	withHeader(b.t, b.repo, n, "# Guards:  "+boundaryGuard+" drop table orders")
}

func (b *boundary) dropped() bool {
	_, err := os.Stat(b.marker)
	return err == nil
}

// runLane runs lane n's script directly with no terminal, and with an
// approval broker when broker isn't nil.
func (b *boundary) runLane(n int, broker *broker) string {
	b.t.Helper()
	c := b.cmd("bash", filepath.Base(b.scriptPath(n)))
	c.SysProcAttr = noTTY()
	if broker != nil {
		// As swim run does: the lane process gets its own token, registered
		// with the guards its script declares.
		tok := broker.Register(approval.Caller{Lane: n, Guards: []string{boundaryGuard}})
		c.Env = append(c.Env, approval.EnvVar+"="+broker.Path(), approval.TokenEnv+"="+tok)
	}
	out, _ := c.CombinedOutput()
	return string(out)
}

func (b *boundary) scriptPath(n int) string {
	return filepath.Join(b.root, "lane."+strconv.Itoa(n)+".sh")
}

func (b *boundary) sha(n int) string {
	b.t.Helper()
	sum, err := chain.HashFile(b.scriptPath(n))
	if err != nil {
		b.t.Fatal(err)
	}
	return sum
}

func (b *boundary) approve(n int) {
	b.t.Helper()
	b.mustSwim("approve", strconv.Itoa(n), "--guards", boundaryGuard, "--key", b.keys["zach"])
}

func (b *boundary) links() []chain.Link {
	b.t.Helper()
	links, err := chain.Read(b.root)
	if err != nil {
		b.t.Fatal(err)
	}
	return links
}

// noMarker fails when the guarded command ran.
func (b *boundary) noMarker(t *testing.T, why string) {
	t.Helper()
	if b.dropped() {
		t.Fatalf("%s: the guarded command ran\n%s", why, b.log(1))
	}
}

func TestSecurity(t *testing.T) {
	cases := []struct {
		name   string
		closes string // the task(s) whose guarantee this case checks
		run    func(t *testing.T)
	}{
		// The execution-boundary fixture: from guard to process launch.
		{"no approval and no terminal: not run", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			c := b.cmd(bin, "run", "1", "--plain")
			c.SysProcAttr = noTTY()
			out, _ := c.CombinedOutput()
			b.noMarker(t, "swim run with no terminal")
			contains(t, "agent1.log", b.log(1), "SKIP  drop table orders (dry run: set "+boundaryGuard+"=1 to approve)", "PASS  after")
			if strings.Contains(string(out), "requires human approval") {
				t.Errorf("prompted without a terminal:\n%s", out)
			}
		}},
		{"declined at the prompt: not run", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			b.runLane(1, newBroker(t, func(approval.Request, <-chan struct{}) bool { return false }))
			b.noMarker(t, "declined")
			contains(t, "agent1.log", b.log(1), "SKIP  drop table orders (dry run: declined at the prompt by tester")
		}},
		{"stopped while waiting for approval: not run", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			br := newBroker(t, func(_ approval.Request, cancel <-chan struct{}) bool { <-cancel; return true })
			c := b.cmd("bash", "lane.1.sh")
			c.SysProcAttr = noTTY()
			tok := br.Register(approval.Caller{Lane: 1, Guards: []string{boundaryGuard}})
			c.Env = append(c.Env, approval.EnvVar+"="+br.Path(), approval.TokenEnv+"="+tok)
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return len(br.requests()) == 1 })
			os.WriteFile(filepath.Join(b.root, ".swim", "lane1.stop"), nil, 0o644)
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the guard kept waiting after the stop request")
			}
			b.noMarker(t, "stopped")
			contains(t, "agent1.log", b.log(1), "not approved: the lane is stopping")
		}},
		{"past the round's Timeout while waiting: not run", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			withHeader(t, b.repo, 1, "# Timeout: 2s")
			// A broker that would approve, but only after the deadline.
			br := newBroker(t, func(_ approval.Request, cancel <-chan struct{}) bool {
				select {
				case <-cancel:
				case <-time.After(20 * time.Second):
				}
				return true
			})
			b.runLane(1, br)
			b.noMarker(t, "timed out")
			contains(t, "agent1.log", b.log(1), "SKIP  drop table orders (dry run: not approved: the round's Timeout ran out")
		}},
		{"an approval for another script doesn't authorize this one", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			b.lane(2, "\nrun \"other\" true")
			b.approve(2)
			// Lane 2's approval file, whatever manifest it names.
			files, _ := filepath.Glob(filepath.Join(sigfile.Dir(b.root), "lane.2."+b.sha(2)[:12]+"*.signature"))
			if len(files) != 1 {
				t.Fatalf("lane 2's approval: %v", files)
			}
			other := files[0]
			data, err := os.ReadFile(other)
			if err != nil {
				t.Fatal(err)
			}
			// Under lane 1's file name, as is: it names lane 2's script.
			mine := sigfile.ScriptPath(b.root, 1, b.sha(1), "")
			os.WriteFile(mine, data, 0o644)
			b.mustSwim("run", "1", "--plain")
			b.noMarker(t, "lane 2's approval under lane 1's name")
			// Rewritten to name lane 1's script: the signatures no longer match.
			forged := strings.ReplaceAll(string(data), b.sha(2), b.sha(1))
			forged = strings.ReplaceAll(forged, "lane.2.sh", "lane.1.sh")
			forged = strings.Replace(forged, "lane: 2", "lane: 1", 1)
			os.WriteFile(mine, []byte(forged), 0o644)
			b.mustSwim("run", "1", "--plain")
			b.noMarker(t, "lane 2's approval rewritten for lane 1")
			// Lane 1's own approval does authorize it (the fixture works).
			os.Remove(mine)
			b.approve(1)
			b.mustSwim("run", "1", "--plain")
			if !b.dropped() {
				t.Fatalf("lane 1's own approval didn't run it:\n%s", b.log(1))
			}
		}},
		{"editing the script after approval needs a fresh approval", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			b.approve(1)
			b.lane(1, "\nrun \"added after approval\" true")
			b.mustSwim("run", "1", "--plain")
			b.noMarker(t, "edited after approval")
			contains(t, "agent1.log", b.log(1), "approves another version of lane.1.sh")
			b.approve(1)
			b.mustSwim("run", "1", "--plain")
			if !b.dropped() {
				t.Fatalf("a fresh approval didn't run it:\n%s", b.log(1))
			}
		}},
		{"the chain's last entry names the script, approval and outcome", "T25", func(t *testing.T) {
			b := newBoundaryRepo(t)
			b.approve(1)
			b.mustSwim("run", "1", "--plain")
			if !b.dropped() {
				t.Fatalf("approved but not run:\n%s", b.log(1))
			}
			links := b.links()
			last := links[len(links)-1]
			if last.Type != "round" || last.Lane != 1 || last.Script != b.sha(1) || last.Result != "passed" || last.Exit == nil || *last.Exit != 0 {
				t.Fatalf("last entry: %+v", last.Entry)
			}
			var appr *chain.Link
			for i := range links {
				if links[i].Type == "approve" && links[i].Lane == 1 && links[i].Run == last.Run {
					appr = &links[i]
				}
			}
			if appr == nil || appr.Guard != boundaryGuard || appr.Script != b.sha(1) || appr.Method != "signature" {
				t.Fatalf("approve entry for the round: %+v", appr)
			}
			if len(last.Approvals) != 1 || last.Approvals[0] != appr.Hash {
				t.Errorf("the round names approvals %v, want [%s]", last.Approvals, appr.Hash)
			}
			contains(t, "verify", b.mustSwim("verify"), "verified: every link")
		}},
		{"a command run without guard has no approval in the chain", "T4", func(t *testing.T) {
			b := newBoundaryRepo(t)
			b.script(2, "unguarded", `run "drop" touch "`+b.marker+`"`)
			b.mustSwim("run", "2", "--plain")
			if !b.dropped() {
				t.Fatalf("the unguarded command didn't run:\n%s", b.log(2))
			}
			var round bool
			for _, l := range b.links() {
				if l.Lane != 2 {
					continue
				}
				switch l.Type {
				case "approve":
					t.Errorf("an approve entry for an unguarded lane: %+v", l.Entry)
				case "round":
					round = true
					if len(l.Approvals) != 0 {
						t.Errorf("the round names approvals: %v", l.Approvals)
					}
				}
			}
			if !round {
				t.Error("no round entry for lane 2")
			}
		}},
		{"a guarded secret is refused without approval", "guarded-secrets", func(t *testing.T) {
			gap(t, "features/guarded-secrets")
		}},
		{"the chain and its signer key replaced: consistent", "T8", func(t *testing.T) {
			b := newBoundaryRepo(t)
			b.approve(1)
			b.mustSwim("run", "1", "--plain")
			// An attacker with write access re-signs every entry with a key
			// of their own and swaps the chain file.
			p := filepath.Join(t.TempDir(), "mallory")
			if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "mallory", "-f", p).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			key, err := sign.LoadKey(p)
			if err != nil {
				t.Fatal(err)
			}
			links := b.links()
			os.Remove(chain.Path(b.root))
			for _, l := range links {
				if _, err := chain.Append(b.root, l.Entry, key); err != nil {
					t.Fatal(err)
				}
			}
			if b.links()[0].Signer == links[0].Signer {
				t.Fatal("the chain wasn't re-signed")
			}
			// Today verify only checks the chain against itself: it's
			// consistent, and the new signer is a warning, not a failure.
			out, code := b.swim("verify")
			if code != 0 {
				t.Fatalf("verify of a re-signed chain: exit %d\n%s", code, out)
			}
			contains(t, "verify", out, "verified: every link", "warning: entries signed by a key not in lookup.yml")
		}},
		{"the chain and its signer key replaced: not trusted, not anchored", "T8", func(t *testing.T) {
			gap(t, "T8")
		}},
		{"a planted secret never appears in the log, .swim.log or status", "T9", func(t *testing.T) {
			r := newRepo(t, "")
			secretsKey(t, r)
			r.setSecret("db_password", "planted-s3cret-value")
			r.script(1, "uses a secret", `DB=$(swim secret get db_password) || stop "no secret"
msg "using it"
run "echo it" bash -c "echo db=$DB; echo db=$DB >&2; printf %s $DB | base64"
run "fail with it" bash -c "echo $DB; exit 3"`)
			out, _ := r.swim("run", "1", "--plain")
			st, _ := os.ReadFile(filepath.Join(r.root, ".swim", "status.yml"))
			hist, _ := os.ReadFile(filepath.Join(r.root, ".swim.log"))
			// The value and its base64 (a prefix that doesn't depend on padding).
			for what, s := range map[string]string{"output": out, "agent1.log": r.log(1), "status.yml": string(st), ".swim.log": string(hist), "status": r.mustSwim("status")} {
				for _, v := range []string{"planted-s3cret-value", "cGxhbnRlZC1zM2NyZXQtdmFsdW"} {
					if strings.Contains(s, v) {
						t.Errorf("%s holds the secret (%s):\n%s", what, v, s)
					}
				}
			}
			contains(t, "agent1.log", r.log(1), "db=***(db_password)")
		}},
		{"a planted secret in every file swim writes", "T9", func(t *testing.T) {
			gap(t, "T9")
		}},

		// Known gaps: each closing task replaces its gap call with the case.
		{"a guard with a misleading description: the prompt shows the true lane", "T3", func(t *testing.T) {
			gap(t, "T3")
		}},
		{"a guard request with the wrong job id: the chain names the true lane", "T3", func(t *testing.T) {
			gap(t, "T3")
		}},
		{"an approval response replayed from another lane is refused", "T3, T10", func(t *testing.T) {
			gap(t, "T3, T10")
		}},
		{"_mark and _finish for another lane are refused", "T3, T10", func(t *testing.T) {
			gap(t, "T3, T10")
		}},
		{"a hostile lane at the broker: concurrent requests, disconnects, oversized messages", "T3", func(t *testing.T) {
			gap(t, "T3")
		}},
		{"an included file changed between approval and exec", "T1, T2", func(t *testing.T) {
			gap(t, "T1, T2")
		}},
		{"the runner changed between approval and exec", "T1, T2", func(t *testing.T) {
			gap(t, "T1, T2")
		}},
		{"the env changed between approval and exec", "T1, T2", func(t *testing.T) {
			gap(t, "T1, T2")
		}},
		{"the evidence changed between approval and exec", "T1, T2", func(t *testing.T) {
			gap(t, "T1, T2")
		}},
		{"a chain record over 256 KiB, then another append", "T24", TestChainLargeEntryAndRepair},
		{"a worker killed mid-step: no false success, no silent re-run", "T6, T7", func(t *testing.T) {
			gap(t, "T6, T7")
		}},
		{"a worker killed mid-chain-append: the chain is repairable", "T6, T7", func(t *testing.T) {
			gap(t, "T6, T7")
		}},
		{"escape sequences in a guard description are shown escaped", "T26", TestUntrustedTextIsSafe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t)
		})
	}
}
