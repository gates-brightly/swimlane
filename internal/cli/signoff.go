package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
	"github.com/gates-brightly/swimlane/internal/trust"
)

// A signoff is the launcher's signed word that a lane, as committed,
// passed on a commit's tree (# Signoff: CONTEXT): run here instead of in
// CI, and posted as a GitHub commit status that branch protection can
// require. Like a chain checkpoint, swim does it after the round, with the
// credentials of whoever runs the launcher; the lane only declares it. It's an
// attestation, not a proof: it shows whose key vouches, not that the
// round ran honestly.

// signoffVersion is the first line of every signoff statement.
const signoffVersion = "swim signoff v1"

// Signoff is one signed statement, as kept in .swim/signoffs/COMMIT/N.json.
type Signoff struct {
	Statement string `json:"statement"`
	Signature string `json:"signature"`
}

// swim signoff verify FILE ...
func cmdSignoff(args []string) error {
	if len(args) == 0 || args[0] != "verify" {
		return usagef("swim signoff verify FILE ...: a lane signs off with a # Signoff: header")
	}
	return signoffVerify(args[1:])
}

// headState is HEAD as a run started: its commit and tree, or why the
// tree wasn't HEAD's (err), so nothing the run did can be signed off.
type headState struct {
	commit, tree string
	err          error
}

func signoffHead(root string) headState {
	c, t, err := cleanHead(root)
	return headState{c, t, err}
}

// signoffsAfterRun signs off each lane of run runID that declares
// # Signoff: and passed: with the key of whoever ran the launcher (swim
// run, or the swimmer under swim watch), posting the commit status with
// their gh login. Approving the script approved this; the lane takes no
// part. Nothing here changes the run's result: a lane that can't be signed
// off says why.
func signoffsAfterRun(root string, cfg *config.Config, runID string, head headState) {
	st, err := status.Load(root)
	if err != nil {
		return
	}
	for n := 1; n <= cfg.Lanes; n++ {
		info, err := lane.ReadScript(root, n)
		if err != nil || info.Signoff == "" {
			continue
		}
		l := st.Get(n)
		if l == nil || l.Run != runID {
			continue
		}
		if err := signoffLane(root, cfg, n, l, info.Signoff, head); err != nil {
			fmt.Fprintf(os.Stderr, "swim %d: %s not signed off: %v\n", n, info.Signoff, err)
		}
	}
}

func signoffLane(root string, cfg *config.Config, n int, l *status.Lane, ctx string, head headState) error {
	if l.State != status.Passed {
		return fmt.Errorf("the round %s", l.State)
	}
	if head.err != nil {
		return fmt.Errorf("when the run started, %v", head.err)
	}
	if err := laneCommitted(root, n); err != nil {
		return err
	}
	if c, _, err := cleanHead(root); err != nil {
		return fmt.Errorf("the run changed the tree: %v", err)
	} else if c != head.commit {
		return fmt.Errorf("HEAD moved from %s to %s during the run", head.commit[:12], c[:12])
	}
	key, err := runnerKey(cfg)
	if err != nil {
		return err
	}
	if key == nil {
		return errors.New("no signing key (signing_key in swim config, or ~/.ssh/id_ed25519)")
	}
	stmt := strings.Join([]string{
		signoffVersion,
		"commit " + head.commit,
		"tree " + head.tree,
		"lane " + strconv.Itoa(n),
		"context " + ctx,
		"script " + l.ScriptSHA,
		"manifest " + l.ManifestSHA,
		"job " + l.Job,
		"run " + l.Run,
		"result " + status.Passed,
		"at " + status.Now(),
		"signer " + sign.KeyOnly(key.Public),
	}, "\n") + "\n"
	sig, err := sign.Sign(key, []byte(stmt))
	if err != nil {
		return err
	}
	path, err := writeSignoff(root, head.commit, n, Signoff{Statement: stmt, Signature: sig})
	if err != nil {
		return err
	}
	fmt.Printf("swim %d: signed off %s on %s: %s\n", n, ctx, head.commit[:12], rel(root, path))
	sum := sha256.Sum256([]byte(stmt))
	desc := fmt.Sprintf("passed (swim run %s); statement sha256:%s", l.Run, hex.EncodeToString(sum[:])[:16])
	err = ghReady(root)
	if err == nil {
		err = postStatus(root, head.commit, ctx, desc)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "  not posted: %v\n", err)
		return nil
	}
	fmt.Printf("  posted commit status %s: success\n", ctx)
	return nil
}

// cleanHead is HEAD's commit and tree, refusing a tree that differs from
// it: a change to a tracked file, or an untracked file git doesn't ignore
// (a lane in the working tree, or one that includes it, could read it).
func cleanHead(root string) (commit, tree string, err error) {
	commit = gitRevParse(root, "HEAD")
	tree = gitRevParse(root, "HEAD^{tree}")
	if commit == "" || tree == "" {
		return "", "", errors.New("a signoff names a commit, and this isn't a git repo with one")
	}
	out, err := gitRead(root, exec.Command("git", "status", "--porcelain"))
	if err != nil {
		return "", "", err
	}
	if out = strings.TrimRight(out, "\n"); out != "" {
		lines := strings.Split(out, "\n")
		more := ""
		if len(lines) > 5 {
			lines, more = lines[:5], fmt.Sprintf("\n  ... and %d more", len(lines)-5)
		}
		return "", "", fmt.Errorf("the tree isn't HEAD's: commit or stash these first (a signoff vouches for the commit, not your working copy):\n  %s%s",
			strings.Join(lines, "\n  "), more)
	}
	return commit, tree, nil
}

// laneCommitted refuses a lane script that isn't in the commit: a reviewer
// can't see what a signoff for an uncommitted lane ran.
func laneCommitted(root string, n int) error {
	name := filepath.Base(lane.Script(root, n))
	if _, err := gitRead(root, exec.Command("git", "ls-files", "--error-unmatch", "--", name)); err != nil {
		return fmt.Errorf("%s isn't committed, so nobody can see what its signoff ran: add !/%s to .gitignore and commit it", name, name)
	}
	return nil
}

func writeSignoff(root, commit string, n int, s Signoff) (string, error) {
	dir := filepath.Join(status.Dir(root), "signoffs", commit)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, strconv.Itoa(n)+".json")
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}

// ghReady checks gh can post a status before a lane spends its time.
func ghReady(root string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errors.New("gh (the GitHub CLI) isn't installed; the signoff is kept only here")
	}
	c := exec.Command("gh", "auth", "status")
	c.Dir = root
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("gh isn't logged in (gh auth login): %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// postStatus sets a success commit status on commit (gh resolves the repo).
func postStatus(root, commit, ctx, desc string) error {
	if len(desc) > 140 { // GitHub's limit
		desc = desc[:140]
	}
	c := exec.Command("gh", "api", "--method", "POST", "repos/{owner}/{repo}/statuses/"+commit,
		"-f", "state=success", "-f", "context="+ctx, "-f", "description="+desc)
	c.Dir = root
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "No commit found") || strings.Contains(msg, "422") {
			msg += " (push the commit first: GitHub only takes a status for a commit it has)"
		}
		return errors.New(msg)
	}
	return nil
}

// swim signoff verify FILE ...: each statement's signature, and that its
// commit (when this repo has it) still has the tree it names.
func signoffVerify(files []string) error {
	if len(files) == 0 {
		return usagef("name the signoff files to verify (.swim/signoffs/COMMIT/N.json)")
	}
	root, _ := os.Getwd()
	if r, _, err := repoNoMigrate(); err == nil {
		root = r
	}
	bad := 0
	for _, f := range files {
		if err := verifySignoff(root, f); err != nil {
			fmt.Printf("%s: NOT VERIFIED: %v\n", f, err)
			bad++
		}
	}
	if bad > 0 {
		return exitError{1}
	}
	return nil
}

func verifySignoff(root, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s Signoff
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("not a signoff: %v", err)
	}
	fields := map[string]string{}
	lines := strings.Split(strings.TrimSuffix(s.Statement, "\n"), "\n")
	if len(lines) == 0 || lines[0] != signoffVersion {
		return fmt.Errorf("not a %q statement", signoffVersion)
	}
	for _, l := range lines[1:] {
		k, v, _ := strings.Cut(l, " ")
		fields[k] = v
	}
	for _, k := range []string{"commit", "tree", "lane", "script", "result", "signer"} {
		if fields[k] == "" {
			return fmt.Errorf("the statement has no %s", k)
		}
	}
	if len(fields["commit"]) < 40 || len(fields["tree"]) < 40 {
		return errors.New("the statement's commit or tree isn't a full object id")
	}
	if err := sign.Verify(fields["signer"], []byte(s.Statement), s.Signature); err != nil {
		return err
	}
	if fields["result"] != status.Passed {
		return fmt.Errorf("result %s", fields["result"])
	}
	// A good signature by a key nobody named vouches for nothing.
	fp := sign.Fingerprint(fields["signer"])
	who := ""
	if lk, err := lookup.Load(root); err == nil {
		if name, ok := lk.Who(fields["signer"]); ok {
			who = name + " in lookup.yml"
		}
	}
	if tf, err := trust.Load(root); who == "" && err == nil && tf != nil && tf.Has(fields["signer"]) {
		who = "a key in trust.yml"
	}
	if who == "" {
		return fmt.Errorf("signature good, but by %s, which is in neither lookup.yml nor trust.yml: nothing says whose key it is", fp)
	}
	tree := ""
	if t, err := gitRead(root, exec.Command("git", "rev-parse", "--verify", "--quiet", fields["commit"]+"^{tree}")); err == nil {
		tree = strings.TrimSpace(t)
	}
	switch {
	case tree == "":
		fmt.Printf("%s: signature good, by %s, %s; commit %s isn't in this repo, so its tree wasn't checked\n",
			path, who, fp, fields["commit"][:12])
	case tree != fields["tree"]:
		return fmt.Errorf("commit %s has tree %s, not the %s signed off", fields["commit"][:12], tree[:12], fields["tree"][:12])
	default:
		fmt.Printf("%s: VERIFIED: %s passed on %s, signed by %s, %s\n",
			path, fields["context"], fields["commit"][:12], who, fp)
	}
	return nil
}

// gitRead runs c, a git command that only reads (policy_test checks the
// subcommand), in root and returns its stdout.
func gitRead(root string, c *exec.Cmd) (string, error) {
	c.Dir = root
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s: %s", strings.Join(c.Args[:2], " "), msg)
		}
		return "", fmt.Errorf("%s: %v", strings.Join(c.Args[:2], " "), err)
	}
	return string(out), nil
}
