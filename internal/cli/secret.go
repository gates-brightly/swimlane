package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
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
	}
	return usagef("usage: swim secret set|get|ls|rm (see swim secret --help)")
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
	fmt.Printf("  lanes get it with: # Secrets: %s   or   _swim.%s in a step   or   swim secret get %s\n", name, name, name)
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

// openVault decrypts the secrets root sees with the operator's key. A
// missing key isn't an error when the store is empty or the env supplies
// (SWIM_SECRET_<NAME>): the vault then resolves only those.
func openVault(root string, cfg *config.Config, ask bool) (*secrets.Vault, *secrets.File, error) {
	f, err := secrets.Load(secretsPath())
	if err != nil {
		return nil, nil, err
	}
	repo := root
	if !inGitRepo(root) {
		repo = ""
	}
	if f.Empty(repo) {
		return secrets.Open(f, repo, nil, os.Getenv), f, nil
	}
	key, err := secrets.LoadKey(cfg.SecretsKeyPath())
	if err != nil {
		return secrets.Open(f, repo, nil, os.Getenv), f, err
	}
	var prompt func() ([]byte, error)
	if ask {
		prompt = passphrasePrompt(key.Path)
	}
	id, err := key.Identity(prompt)
	if err != nil {
		return secrets.Open(f, repo, nil, os.Getenv), f, err
	}
	return secrets.Open(f, repo, id, os.Getenv), f, nil
}

func secretGet(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return usagef("usage: swim secret get NAME")
	}
	name := args[0]
	var value string
	// Inside a swim run: ask its broker (the run decrypted at start).
	lane, _ := strconv.Atoi(os.Getenv("SWIM_LANE"))
	resp, inRun, err := secrets.Call(os.Getenv, secrets.Request{Op: "get", Name: name, Lane: lane, How: "get"})
	switch {
	case inRun && err != nil:
		return fmt.Errorf("secret %s: %v", name, err)
	case inRun:
		value = resp.Value
	default:
		root, cfg, err := repoNoMigrate()
		if err != nil {
			return err
		}
		v, _, openErr := openVault(root, cfg, true)
		s, err := v.Get(name)
		if err != nil {
			if openErr != nil {
				return fmt.Errorf("secret %s: %v", name, openErr)
			}
			return err
		}
		value = s.Value
	}
	fmt.Print(value)
	if ui.IsTTY(os.Stdout) {
		fmt.Println()
	}
	return nil
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
	repo := root
	if !inGitRepo(root) {
		repo = ""
	}
	ls := f.List(repo)
	cur := ""
	if k, err := secrets.LoadKey(cfg.SecretsKeyPath()); err == nil {
		cur = k.Fingerprint
	}
	if asYAML {
		fmt.Println("secrets:")
		for _, l := range ls {
			fmt.Printf("  - name: %s\n    scope: %s\n    key: %s\n    updated: %s\n    shadowed: %v\n", l.Name, l.Scope, l.Key, l.Updated, l.Shadowed)
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
