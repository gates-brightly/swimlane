package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/launcher"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/secrets"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// cmdSecret is swim secret set|get|ls|rm: secrets encrypted to the
// operator's SSH key, for every repo (--all) or this git repo (--repo).
func cmdSecret(args []string) error {
	if len(args) == 0 {
		return secretList(nil)
	}
	switch args[0] {
	case "set":
		return secretSet(args[1:])
	case "get":
		return secretGet(args[1:])
	case "ls", "list":
		return secretList(args[1:])
	case "rm", "remove":
		return secretRemove(args[1:])
	case "grant":
		return secretGrant(args[1:])
	case "revoke":
		return secretRevoke(args[1:])
	case "grants":
		return secretGrants(args[1:])
	}
	return usagef("usage: swim secret set|get|ls|rm|grant|revoke|grants (see swim secret --help)")
}

// secretScope reads --all / --repo: the repo scope's key ("" for all). The
// default is this git repo; outside one, --all must be said.
func secretScope(root string, all, repoFlag bool) (string, error) {
	if all && repoFlag {
		return "", usagef("use --all or --repo, not both")
	}
	if all {
		return "", nil
	}
	if !inGitRepo(root) {
		if repoFlag {
			return "", fmt.Errorf("%s isn't in a git repo: --repo scopes a secret to one; use --all", root)
		}
		return "", fmt.Errorf("not in a git repo: say --all to store it for every repo")
	}
	return root, nil
}

func inGitRepo(root string) bool {
	c := exec.Command("git", "rev-parse", "--show-toplevel")
	c.Dir = root
	out, err := c.Output()
	return err == nil && filepath.Clean(strings.TrimSpace(string(out))) == filepath.Clean(root)
}

func secretsPath() string { return secrets.Path(config.Path()) }

func scopeWord(repo string) string {
	if repo == "" {
		return "all repos"
	}
	return "repo " + repo
}

func secretSet(args []string) error {
	var all, repoFlag bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "repo": &repoFlag}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim secret set NAME [--all|--repo]   (the value comes from a hidden prompt or stdin, never the command line)")
	}
	name := rest[0]
	if !secrets.ValidName(name) {
		return fmt.Errorf("%q isn't a secret name: use a letter, then letters, digits and _", name)
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	repo, err := secretScope(root, all, repoFlag)
	if err != nil {
		return err
	}
	key, err := secrets.LoadKey(cfg.SecretsKeyPath())
	if err != nil {
		return err
	}
	value, err := readSecretValue(name)
	if err != nil {
		return err
	}
	f, err := secrets.Load(secretsPath())
	if err != nil {
		return err
	}
	if err := f.Set(key, repo, name, value); err != nil {
		return err
	}
	if err := f.Save(secretsPath()); err != nil {
		return err
	}
	if repo != "" {
		history.Log(root, history.Entry{Event: history.SecretSet, Detail: name})
	}
	fmt.Printf("stored %s for %s (encrypted to %s, %s)\n", name, scopeWord(repo), key.Fingerprint, filepath.Base(key.Path))
	fmt.Printf("  lanes get it with: $(swim secret get %s)   or, in an SDK lane: secret.get(%q)\n", name, name)
	return nil
}

// readSecretValue reads a value from a hidden prompt on a terminal, or all
// of stdin (one trailing newline dropped).
func readSecretValue(name string) (string, error) {
	if ui.IsTTY(os.Stdin) {
		fmt.Fprintf(os.Stderr, "value for %s (hidden): ", name)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if len(b) == 0 {
			return "", errors.New("empty value: nothing stored")
		}
		return string(b), nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	if v == "" {
		return "", errors.New("empty value on stdin: nothing stored")
	}
	return v, nil
}

// passphrasePrompt asks for the secrets key's passphrase on the terminal;
// nil when nobody is there.
func passphrasePrompt(path string) func() ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	return func() ([]byte, error) {
		defer tty.Close()
		fmt.Fprintf(tty, "passphrase for %s (swim secrets): ", path)
		b, err := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		return b, err
	}
}

// openVault decrypts the secrets root sees with the operator's key, and
// adds the hub's grants (returned too, for their guards); ask allows a
// passphrase prompt on the terminal.
func openVault(root string, cfg *config.Config, ask bool) (*secrets.Vault, map[string]secrets.Granted, error) {
	var prompt func() ([]byte, error)
	if ask {
		if k, err := secrets.LoadKey(cfg.SecretsKeyPath()); err == nil {
			prompt = passphrasePrompt(k.Path)
		}
	}
	v, _, err := secrets.OpenStore(secretsPath(), secrets.RepoKey(root), cfg.SecretsKeyPath(), prompt, os.Getenv)
	granted, hubErr := hubGrants(root)
	v.Add(secrets.ScopeHub, secrets.Values(granted))
	if err == nil && hubErr != nil {
		err = hubErr
	}
	return v, granted, err
}

func secretGet(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return usagef("usage: swim secret get NAME")
	}
	name := args[0]
	// A module asks only for the secrets its module.yml declares (the
	// broker still decides whether the lane gets them).
	if mod := os.Getenv(moduleEnv); mod != "" {
		declared := false
		for _, s := range strings.Fields(os.Getenv(moduleSecretsEnv)) {
			declared = declared || strings.EqualFold(s, name)
		}
		if !declared {
			return fmt.Errorf("secret %s: module %s doesn't declare it (its module.yml effects: secrets:)", name, mod)
		}
	}
	var value string
	// Inside a swim run: ask its broker (the run decrypted at start). It
	// says which lane asks, from the lane process's token, and releases a
	// guarded secret only once its guard was approved for this lane.
	resp, inRun, err := secrets.Call(os.Getenv, secrets.Request{Op: "get", Name: name, How: "get"})
	switch {
	case inRun && err != nil:
		if strings.HasPrefix(err.Error(), "refused: ") {
			return secretRefused(name, err.Error())
		}
		return fmt.Errorf("secret %s: %v", name, err)
	case inRun:
		value = resp.Value
	default:
		root, cfg, err := repoNoMigrate()
		if err != nil {
			return err
		}
		// A lane outside a swim run (run directly with bash) has no broker
		// to approve its guard: a guarded secret is the run's to release.
		inLane := os.Getenv("SWIM_RUN") != "" || os.Getenv("SWIM_LANE") != ""
		refuse := func(flags []string) error {
			return secretRefused(name, fmt.Sprintf("refused: %s is guarded by %s: a lane gets it only from its swim run, once that guard is approved there", name, strings.Join(flags, " or ")))
		}
		if g := cfg.SecretGuard(name); inLane && len(g) > 0 {
			return refuse(g) // before decrypting anything
		}
		v, granted, openErr := openVault(root, cfg, true)
		guard := secrets.PolicyFor(launcher.Policies(cfg, v, granted), name)
		if inLane && guard.Guarded() {
			return refuse(guard.Flags()) // a hub grant's guard
		}
		s, err := v.Get(name)
		if err != nil {
			if openErr != nil {
				return fmt.Errorf("secret %s: %v", name, openErr)
			}
			return err
		}
		value = s.Value
		if guard.Guarded() {
			// The operator at their own shell: allowed, and recorded.
			history.Log(root, history.Entry{Event: history.SecretUse, Detail: fmt.Sprintf("%s (%s scope, guarded by %s; got outside a run by %s)", name, s.Scope, strings.Join(guard.Flags(), " or "), operator())})
		}
	}
	fmt.Print(value)
	if ui.IsTTY(os.Stdout) {
		fmt.Println()
	}
	return nil
}

// secretRefused reports a guarded secret refused to a lane: a WARN line in
// the lane's log (bash lanes: the run's broker records it in .swim.log and
// the chain), and the refusal as the error.
func secretRefused(name, why string) error {
	if n := os.Getenv("SWIM_LANE"); n != "" {
		cmdMark([]string{n, logparse.Warn, "secret " + name + " refused", strings.TrimPrefix(why, "refused: ")})
	}
	return errors.New(why)
}

func secretList(args []string) error {
	var asYAML bool
	rest, err := flags{bools: map[string]*bool{"yaml": &asYAML}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim secret ls [--yaml]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	f, err := secrets.Load(secretsPath())
	if err != nil {
		return err
	}
	ls := f.List(secrets.RepoKey(root))
	cur := ""
	if k, err := secrets.LoadKey(cfg.SecretsKeyPath()); err == nil {
		cur = k.Fingerprint
	}
	if asYAML {
		fmt.Println("secrets:")
		for _, l := range ls {
			fmt.Printf("  - name: %s\n    scope: %s\n    key: %s\n    updated: %s\n    shadowed: %v\n", l.Name, l.Scope, l.Key, l.Updated, l.Shadowed)
			if g := cfg.SecretGuard(l.Name); len(g) > 0 {
				fmt.Printf("    guard: [%s]\n", strings.Join(g, ", "))
			}
		}
		return nil
	}
	if len(ls) == 0 {
		fmt.Println("no secrets (swim secret set NAME [--all|--repo])")
		return nil
	}
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Printf("%-24s %-6s %-21s %s\n", "name", "scope", "updated", "note")
	for _, l := range ls {
		var notes []string
		if l.Shadowed {
			notes = append(notes, "hidden by this repo's own")
		}
		if cur != "" && l.Key != cur {
			notes = append(notes, "encrypted to another key ("+l.Key+")")
		}
		if g := cfg.SecretGuard(l.Name); len(g) > 0 {
			notes = append(notes, "guarded by "+strings.Join(g, " or ")+" (swim.yml)")
		}
		line := fmt.Sprintf("%-24s %-6s %-21s %s", l.Name, l.Scope, l.Updated, strings.Join(notes, "; "))
		if l.Shadowed {
			line = p.Paint(ui.Dim, line)
		}
		fmt.Println(line)
	}
	fmt.Printf("store: %s\n", secretsPath())
	return nil
}

func secretRemove(args []string) error {
	var all, repoFlag bool
	rest, err := flags{bools: map[string]*bool{"all": &all, "repo": &repoFlag}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim secret rm NAME [--all|--repo]")
	}
	root, _, err := repoNoMigrate()
	if err != nil {
		return err
	}
	repo, err := secretScope(root, all, repoFlag)
	if err != nil {
		return err
	}
	f, err := secrets.Load(secretsPath())
	if err != nil {
		return err
	}
	if !f.Remove(repo, rest[0]) {
		return fmt.Errorf("no secret %s for %s", rest[0], scopeWord(repo))
	}
	if err := f.Save(secretsPath()); err != nil {
		return err
	}
	if repo != "" {
		history.Log(root, history.Entry{Event: history.SecretRm, Detail: rest[0]})
	}
	fmt.Printf("removed %s from %s\n", rest[0], scopeWord(repo))
	return nil
}
