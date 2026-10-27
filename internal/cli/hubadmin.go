package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lookup"
)

// The operator's side of a hub: admitting swimmers, approving their keys,
// granting them secrets and managing users, with the hub's admin token or
// an admin user's. swim auth login URL --admin-token T (or --token T)
// saves the URL and token (and --ca's PEM); --hub / --admin-token / --token
// (or SWIM_HUB_URL / SWIM_HUB_ADMIN_TOKEN, and SWIM_HUB_CA, a file)
// override it.

type hubLogin struct {
	URL        string `yaml:"url"`
	AdminToken string `yaml:"admin_token"`
	CA         string `yaml:"ca,omitempty"`
}

func hubLoginPath() string { return filepath.Join(filepath.Dir(config.Path()), "hub-admin.yml") }

// saveHubAdmin checks token against the hub at url (the admin token, or an
// admin user's) and saves both for swim auth admit, users, approve and
// swim secret grant.
func saveHubAdmin(url, token, ca string) error {
	a, err := hub.NewAdminTLS(url, token, []byte(ca))
	if err != nil {
		return fmt.Errorf("hub CA: %w", err)
	}
	if _, err := a.Grants(); err != nil {
		return fmt.Errorf("the hub refused that token as an admin's (the admin token, or an admin user's): %w", err)
	}
	data, _ := yaml.Marshal(hubLogin{URL: strings.TrimRight(url, "/"), AdminToken: token, CA: ca})
	os.MkdirAll(filepath.Dir(hubLoginPath()), 0o700)
	return os.WriteFile(hubLoginPath(), data, 0o600)
}

// readHubAdmin is the saved admin login, if any.
func readHubAdmin() (*hubLogin, error) {
	data, err := os.ReadFile(hubLoginPath())
	if err != nil {
		return nil, err
	}
	var l hubLogin
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// hubAdmin is the operator's admin client: from flags, the env, or swim
// auth login --admin-token.
func hubAdmin(url, token string) (*hub.Admin, string, error) {
	if url == "" {
		url = os.Getenv("SWIM_HUB_URL")
	}
	if token == "" {
		token = os.Getenv("SWIM_HUB_ADMIN_TOKEN")
	}
	var ca string
	if f := os.Getenv("SWIM_HUB_CA"); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, "", fmt.Errorf("SWIM_HUB_CA: %w", err)
		}
		ca = string(data)
	}
	if data, err := os.ReadFile(hubLoginPath()); err == nil {
		var l hubLogin
		if yaml.Unmarshal(data, &l) == nil {
			if url == "" {
				url = l.URL
			}
			if token == "" {
				token = l.AdminToken
			}
			// The saved CA is for the saved hub only.
			if ca == "" && strings.TrimRight(url, "/") == l.URL {
				ca = l.CA
			}
		}
	}
	if url == "" || token == "" {
		return nil, "", fmt.Errorf("no hub admin: swim auth login URL --admin-token T (or an admin user's --token T), or pass --hub URL --admin-token T")
	}
	a, err := hub.NewAdminTLS(url, token, []byte(ca))
	if err != nil {
		return nil, "", fmt.Errorf("hub CA: %w", err)
	}
	return a, url, nil
}

// hubWorkerRef finds a worker by id or name.
func hubWorkerRef(a interface{ Workers() ([]hub.View, error) }, ref string) (hub.View, error) {
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
		return hub.View{}, fmt.Errorf("no swimmer %q on the hub (swimmers: %s)", ref, or(strings.Join(names, ", "), "none"))
	}
	var ids []string
	for _, w := range byName {
		ids = append(ids, w.ID+" ("+w.Host+":"+w.Root+")")
	}
	return hub.View{}, fmt.Errorf("%d swimmers are called %s: name one by id: %s", len(byName), ref, strings.Join(ids, ", "))
}

func hubApproveCmd(args []string, off bool) error {
	var url, token string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token, "token": &token}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim auth approve|unapprove SWIMMER [--hub URL] [--admin-token T | --token T]")
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
		fmt.Printf("%s (%s:%s) key %s: check it matches what swim printed there when it joined\n", w.Name, w.Host, w.Root, w.KeyFP)
	}
	msg, err := a.Approve(w.ID, off)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// hubAdmitCmd lets a worker waiting to join the hub in (admit), or turns it
// away (reject), with the admin token or an admin user's.
func hubAdmitCmd(args []string, reject bool) error {
	var url, token string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token, "token": &token}}.parse(args)
	if err != nil {
		return err
	}
	a, hubURL, err := hubAdmin(url, token)
	if err != nil {
		return err
	}
	waiting, err := a.Pending()
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		if len(waiting) == 0 {
			fmt.Printf("no swimmers waiting to join %s\n", hubURL)
			return nil
		}
		for _, p := range waiting {
			fmt.Printf("%-16s %s  %s:%s  %s  access %s  key %s  from %s\n", p.Name, p.ID, p.Host, p.Root, or(p.Namespace, "-"), or(p.Access, "?"), or(p.KeyFP, "none"), or(p.From, "?"))
		}
		fmt.Println("admit one: swim auth admit NAME (or its id); turn it away: swim auth reject NAME")
		return nil
	}
	if len(rest) != 1 {
		return usagef("usage: swim auth admit|reject [SWIMMER] [--hub URL] [--admin-token T | --token T]   (no SWIMMER: list those waiting)")
	}
	var match []hub.PendingWorker
	for _, p := range waiting {
		if p.ID == rest[0] || p.Name == rest[0] {
			match = append(match, p)
		}
	}
	switch len(match) {
	case 0:
		return fmt.Errorf("no swimmer %q waiting to join %s (swim auth admit lists them)", rest[0], hubURL)
	case 1:
	default:
		return fmt.Errorf("%d swimmers named %s are waiting: give its id (swim auth admit lists them)", len(match), rest[0])
	}
	p := match[0]
	if !reject {
		fmt.Printf("%s (%s:%s, namespace %s, access %s) key %s: check it matches what swim printed there when it joined\n", p.Name, p.Host, p.Root, or(p.Namespace, "-"), or(p.Access, "?"), or(p.KeyFP, "none"))
	}
	msg, err := a.Admit(p.ID, reject)
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// hubUserCmd manages the hub's users with the admin token: each gets a
// token (shown once) to log in with, and a role.
//
//	swim auth users add NAME --role viewer|operator|admin
//	swim auth users remove NAME
//	swim auth users list
func hubUserCmd(args []string) error {
	var url, token, role string
	rest, err := flags{strs: map[string]*string{"hub": &url, "admin-token": &token, "token": &token, "role": &role}}.parse(args)
	if err != nil {
		return err
	}
	usage := usagef("usage: swim auth users add NAME --role viewer|operator|admin | remove NAME | list   [--hub URL] [--admin-token T | --token T]")
	if len(rest) == 0 {
		return usage
	}
	sub, rest := rest[0], rest[1:]
	switch {
	case (sub == "add" || sub == "remove" || sub == "rm") && len(rest) != 1,
		(sub == "list" || sub == "ls") && len(rest) != 0:
		return usage
	case sub == "add" && role != hub.RoleViewer && role != hub.RoleOperator && role != hub.RoleAdmin:
		return usagef("swim auth users add needs --role viewer (reads full logs), --role operator (also interrupts, drains and runs) or --role admin (also manages users, approvals and grants)")
	}
	a, hubURL, err := hubAdmin(url, token)
	if err != nil {
		return err
	}
	switch sub {
	case "add":
		name := rest[0]
		tok, err := a.AddUser(name, role)
		if err != nil {
			return err
		}
		fmt.Printf("added %s (%s) to %s; their token, shown this once (the hub keeps only its hash):\n  %s\n", name, role, hubURL, tok)
		fmt.Printf("they log in at %s/login with it (it lasts until swim auth users remove %s), or send it as Authorization: Bearer\n", hubURL, name)
		// The name should be the one lookup.yml knows them by: it's who a
		// worker records, and (with hub approvals) whose keys count.
		if root, _, err := repoNoMigrate(); err == nil {
			if lk, err := lookup.Load(root); err == nil && lk.Path != "" {
				if _, ok := lk.Users[name]; !ok {
					fmt.Fprintf(os.Stderr, "swim auth: note: %s isn't a user in %s: give them the name lookup.yml knows them by\n", name, rel(root, lk.Path))
				}
			}
		}
		return nil
	case "remove", "rm":
		if err := a.RemoveUser(rest[0]); err != nil {
			return err
		}
		fmt.Printf("removed %s from %s: their token and sessions stop working\n", rest[0], hubURL)
		return nil
	case "list", "ls":
		us, err := a.Users()
		if err != nil {
			return err
		}
		if len(us) == 0 {
			fmt.Printf("no users on %s (swim auth users add NAME --role viewer|operator|admin)\n", hubURL)
			return nil
		}
		fmt.Printf("%-20s %-9s %s\n", "user", "role", "added")
		for _, u := range us {
			fmt.Printf("%-20s %-9s %s\n", u.Name, u.Role, u.Added)
		}
		return nil
	}
	return usage
}
