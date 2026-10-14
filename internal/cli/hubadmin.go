package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/secrets"
)

// The operator's side of a hub: approving workers and granting them
// secrets, with the hub's admin token. swim hub login saves the URL and
// token; --hub / --admin-token (or SWIM_HUB_URL / SWIM_HUB_ADMIN_TOKEN)
// override it.

type hubLogin struct {
	URL        string `yaml:"url"`
	AdminToken string `yaml:"admin_token"`
}

func hubLoginPath() string { return filepath.Join(filepath.Dir(config.Path()), "hub-admin.yml") }

func hubLoginCmd(args []string) error {
	var token string
	rest, err := flags{strs: map[string]*string{"admin-token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || token == "" {
		return usagef("usage: swim hub login URL --admin-token T   (the hub keeps it in its state dir: admin-token)")
	}
	a := hub.NewAdmin(rest[0], token)
	if _, err := a.Grants(); err != nil {
		return fmt.Errorf("the hub refused the admin token: %w", err)
	}
	data, _ := yaml.Marshal(hubLogin{URL: strings.TrimRight(rest[0], "/"), AdminToken: token})
	os.MkdirAll(filepath.Dir(hubLoginPath()), 0o700)
	if err := os.WriteFile(hubLoginPath(), data, 0o600); err != nil {
		return err
	}
	fmt.Printf("logged in to %s as its admin (%s)\n", rest[0], hubLoginPath())
	return nil
}

// hubAdmin is the operator's admin client: from flags, the env, or swim
// hub login.
func hubAdmin(url, token string) (*hub.Admin, string, error) {
	if url == "" {
		url = os.Getenv("SWIM_HUB_URL")
	}
	if token == "" {
		token = os.Getenv("SWIM_HUB_ADMIN_TOKEN")
	}
	if url == "" || token == "" {
		if data, err := os.ReadFile(hubLoginPath()); err == nil {
			var l hubLogin
			if yaml.Unmarshal(data, &l) == nil {
				if url == "" {
					url = l.URL
				}
				if token == "" {
					token = l.AdminToken
				}
			}
		}
	}
	if url == "" || token == "" {
		return nil, "", fmt.Errorf("no hub admin: swim hub login URL --admin-token T (or --hub URL --admin-token T)")
	}
	return hub.NewAdmin(url, token), url, nil
}

// hubWorkerRef finds a worker by id or name.
func hubWorkerRef(a *hub.Admin, ref string) (hub.View, error) {
	ws, err := a.Workers()
	if err != nil {
		return hub.View{}, err
	}
	var byName []hub.View
	var names []string
	for _, w := range ws {
		if w.ID == ref {
			return w, nil
		}
		if w.Name == ref {
			byName = append(byName, w)
		}
		names = append(names, w.Name)
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return hub.View{}, fmt.Errorf("no worker %q on the hub (workers: %s)", ref, or(strings.Join(names, ", "), "none"))
	}
	var ids []string
	for _, w := range byName {
		ids = append(ids, w.ID+" ("+w.Host+":"+w.Root+")")
	}
	return hub.View{}, fmt.Errorf("%d workers are called %s: name one by id: %s", len(byName), ref, strings.Join(ids, ", "))
}

func hubApproveCmd(args []string, off bool) error {
	var url, token string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim hub approve|unapprove WORKER [--hub URL] [--admin-token T]")
	}
	a, _, err := hubAdmin(url, token)
	if err != nil {
		return err
	}
	w, err := hubWorkerRef(a, rest[0])
	if err != nil {
		return err
	}
	if !off && w.KeyFP != "" {
		fmt.Printf("%s (%s:%s) key %s: check it matches what swim hub enroll printed there\n", w.Name, w.Host, w.Root, w.KeyFP)
	}
	msg, err := a.Approve(w.ID, off)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// secretGrant encrypts each named secret, from the operator's store as the
// current repo sees it, to the worker's key and gives the hub only that.
func secretGrant(args []string) error {
	var url, token string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return usagef("usage: swim secret grant WORKER NAME... [--hub URL] [--admin-token T]")
	}
	a, hubURL, err := hubAdmin(url, token)
	if err != nil {
		return err
	}
	w, err := hubWorkerRef(a, rest[0])
	if err != nil {
		return err
	}
	key, fp, approved, err := a.WorkerKey(w.ID)
	if err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("%s enrolled without a key: run swim hub enroll there again (a current swim)", w.Name)
	}
	if !approved {
		return fmt.Errorf("%s isn't approved on the hub: approve it first (a on its row, or swim hub approve %s), after checking its key %s", w.Name, w.Name, fp)
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	v, openErr := openVault(root, cfg, true)
	for _, name := range rest[1:] {
		s, err := v.Get(name)
		if err != nil {
			if openErr != nil {
				err = openErr
			}
			return fmt.Errorf("secret %s: %v", name, err)
		}
		ct, err := secrets.EncryptTo(key, s.Value)
		if err != nil {
			return err
		}
		if err := a.Grant(w.ID, name, ct, fp, operator()); err != nil {
			return err
		}
		fmt.Printf("granted %s (%s scope here) to %s, encrypted to its key %s\n", name, s.Scope, w.Name, fp)
	}
	fmt.Printf("  its runs get them from %s at start: swim secret get NAME / secret.get(NAME)\n", hubURL)
	return nil
}

func secretRevoke(args []string) error {
	var url, token string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return usagef("usage: swim secret revoke WORKER NAME... [--hub URL] [--admin-token T]")
	}
	a, _, err := hubAdmin(url, token)
	if err != nil {
		return err
	}
	w, err := hubWorkerRef(a, rest[0])
	if err != nil {
		return err
	}
	for _, name := range rest[1:] {
		if err := a.Revoke(w.ID, name); err != nil {
			return err
		}
		fmt.Printf("revoked %s from %s (its next run can't get it; rotate the secret if it mattered)\n", name, w.Name)
	}
	return nil
}

func secretGrants(args []string) error {
	var url, token string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim secret grants [--hub URL] [--admin-token T]")
	}
	a, hubURL, err := hubAdmin(url, token)
	if err != nil {
		return err
	}
	gs, err := a.Grants()
	if err != nil {
		return err
	}
	if len(gs) == 0 {
		fmt.Printf("no grants on %s (swim secret grant WORKER NAME)\n", hubURL)
		return nil
	}
	fmt.Printf("%-16s %-24s %-21s %s\n", "worker", "secret", "granted", "by")
	for _, g := range gs {
		fmt.Printf("%-16s %-24s %-21s %s\n", g.Name, g.Secret, g.At, g.By)
	}
	return nil
}

// hubGrants decrypts the secrets the hub granted to this directory (nil
// when it isn't enrolled, or the hub has none for it).
func hubGrants(root string) (map[string]string, error) {
	h, err := loadHub(root)
	if err != nil {
		return nil, nil
	}
	c := hubClient(h)
	fs, err := c.FetchGrants()
	if err != nil {
		return nil, fmt.Errorf("hub secrets from %s: %w", h.URL, err)
	}
	if len(fs) == 0 {
		return nil, nil
	}
	id, err := secrets.WorkerKey(workerKeyPath(root))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range fs {
		v, err := secrets.DecryptWith(f.Ciphertext, id)
		if err != nil {
			return nil, fmt.Errorf("hub secret %s: %v (encrypted to another key: grant it again)", f.Name, err)
		}
		out[f.Name] = v
	}
	return out, nil
}
