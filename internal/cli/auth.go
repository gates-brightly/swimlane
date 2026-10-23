package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/history"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/version"
)

// swim auth: everything about a hub but serving it. A machine joins a hub
// once, for every directory of this user (~/.config/swim/auth.yml,
// private), and becomes a swimmer once the hub's admin admits it: any
// directory where swim watch runs then joins with the machine's token, is
// admitted at once and reports (swim.yml's hub: {name, namespace} names
// it). Logging out removes the machine from the hub, with every directory
// that joined through it. The admin's side (admit, reject, approve,
// users) uses the hub's admin token or an admin user's token, saved by
// swim auth login --admin-token / --token (~/.config/swim/hub-admin.yml).
//
//	swim auth login URL [--access read|readwrite] [--name N] [--ca FILE] [--insecure] [--admin-token T | --token T]
//	swim auth status
//	swim auth logout [--admin]
//	swim auth admit|reject [SWIMMER]
//	swim auth approve|unapprove SWIMMER
//	swim auth users add|remove|list

// authFile is ~/.config/swim/auth.yml.
type authFile struct {
	URL    string `yaml:"url"`
	ID     string `yaml:"id"`
	Token  string `yaml:"token"` // the machine's token
	Name   string `yaml:"name"`
	User   string `yaml:"user"`
	Access string `yaml:"access"` // what its directories enroll with
	CA     string `yaml:"ca,omitempty"`
}

func authPath() string { return filepath.Join(filepath.Dir(config.Path()), "auth.yml") }

func loadAuth() (*authFile, error) {
	data, err := os.ReadFile(authPath())
	if err != nil {
		return nil, err
	}
	var a authFile
	if err := yaml.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", authPath(), err)
	}
	if a.URL == "" || a.Token == "" {
		return nil, fmt.Errorf("%s: no hub (swim auth login URL)", authPath())
	}
	return &a, nil
}

func saveAuth(a *authFile) error {
	data, _ := yaml.Marshal(a)
	if err := os.MkdirAll(filepath.Dir(authPath()), 0o700); err != nil {
		return err
	}
	tmp := authPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, authPath())
}

func cmdAuth(args []string) error {
	if len(args) == 0 {
		return authStatus(nil)
	}
	switch args[0] {
	case "login":
		return authLogin(args[1:])
	case "status":
		return authStatus(args[1:])
	case "logout":
		return authLogout(args[1:])
	case "admit":
		return hubAdmitCmd(args[1:], false)
	case "reject":
		return hubAdmitCmd(args[1:], true)
	case "approve":
		return hubApproveCmd(args[1:], false)
	case "unapprove":
		return hubApproveCmd(args[1:], true)
	case "users", "user":
		return hubUserCmd(args[1:])
	}
	return usagef("usage: swim auth login URL | status | logout | admit | reject | approve | unapprove | users   (swim auth --help)")
}

func machineUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func authLogin(args []string) error {
	var name, caFile, access, adminToken string
	var insecure bool
	rest, err := flags{bools: map[string]*bool{"insecure": &insecure},
		strs: map[string]*string{"name": &name, "ca": &caFile, "access": &access, "admin-token": &adminToken, "token": &adminToken}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usagef("usage: swim auth login URL [--access read|readwrite] [--name N] [--ca FILE] [--insecure] [--admin-token T | --token T]")
	}
	if access != "" && !config.ValidAccess(access) {
		return usagef("--access is read (the hub shows this machine's directories and fetches their logs) or readwrite (it also sends every command)")
	}
	ca, err := hubCA(rest[0], caFile, insecure)
	if err != nil {
		return err
	}
	c, err := hub.NewClientTLS(rest[0], "", "", []byte(ca))
	if err != nil {
		return fmt.Errorf("--ca %s: %w", caFile, err)
	}
	// An admin's login: saved for swim auth admit, users, approve and
	// swim secret grant. It joins this machine as a swimmer only with
	// --access (an operator's laptop needn't run lanes for the hub).
	if adminToken != "" {
		if err := saveHubAdmin(c.URL, adminToken, ca); err != nil {
			return err
		}
		fmt.Printf("logged in to %s as its admin (%s)\n", c.URL, hubLoginPath())
		if access == "" {
			fmt.Println("this machine isn't a swimmer: add --access read|readwrite to have its directories report to the hub too")
			return nil
		}
	}
	// Logged in to this hub already: its own token logs in again.
	token := ""
	old, _ := loadAuth()
	if old != nil && strings.TrimRight(old.URL, "/") == c.URL {
		token = old.Token
		if access == "" {
			access = old.Access
		}
	}
	if access == "" {
		if access, err = askAccess(); err != nil {
			return err
		}
	}
	host, _ := os.Hostname()
	if name == "" {
		name = strings.Split(host, ".")[0]
	}
	who := machineUser()
	id, tok, pending, err := c.EnrollMachine(hub.Machine{Name: name, Host: host, User: who, Access: access, Version: version.String()}, token)
	if err != nil {
		return err
	}
	if old != nil && strings.TrimRight(old.URL, "/") != c.URL {
		fmt.Printf("logged out of %s here (this machine logs in to one hub; swim auth logout there removed nothing on it)\n", old.URL)
	}
	if err := saveAuth(&authFile{URL: c.URL, ID: id, Token: tok, Name: name, User: who, Access: access, CA: ca}); err != nil {
		return err
	}
	fmt.Printf("logged in to %s as swimmer %s (%s@%s, id %s), access %s\n", c.URL, name, who, host, id, access)
	if pending && adminToken != "" {
		// An admin admits their own machine.
		if a, _, err := hubAdmin(c.URL, adminToken); err == nil {
			if _, err := a.Admit(id, false); err == nil {
				pending = false
				fmt.Println("admitted with your admin login")
			}
		}
	}
	if pending {
		fmt.Printf("waiting for the hub's admin to admit this machine: on the hub's Admin page, or swim auth admit %s\n", name)
	}
	if old != nil && strings.TrimRight(old.URL, "/") == c.URL && old.Access != access {
		// Each directory that joined through the machine tells the hub now,
		// as its own report would: the hub drops what it may no longer send.
		told := 0
		seen := map[string]bool{}
		dirs := []string{}
		if root, _, err := repoNoMigrate(); err == nil {
			dirs = append(dirs, root)
		}
		for _, e := range runnerEntries() {
			dirs = append(dirs, e.Root)
		}
		for _, root := range dirs {
			if seen[root] {
				continue
			}
			seen[root] = true
			h, err := loadHub(root)
			cfg, cerr := loadCfg(root)
			if err != nil || cerr != nil || h.Via != viaAuth {
				continue
			}
			if hubClient(h).Report(hubSnapshot(root, cfg, h.Name, hubNamespace(root, h), "access changed to "+access, pollEvery)) == nil {
				history.Log(root, history.Entry{Event: history.Hub, Detail: "hub access " + access + " for " + h.URL + " (swim auth login)"})
				told++
			}
		}
		fmt.Printf("access is now %s for every directory that joined through this machine (%d told the hub now; the rest at their next report)\n", access, told)
	}
	fmt.Println("every directory where swim watch runs now reports to it; swim auth logout undoes it")
	return nil
}

func authStatus(args []string) error {
	if len(args) != 0 {
		return usagef("usage: swim auth status")
	}
	if l, err := readHubAdmin(); err == nil {
		fmt.Printf("admin:   logged in to %s as its admin (%s)\n", l.URL, hubLoginPath())
	}
	a, err := loadAuth()
	if err != nil {
		fmt.Println("not logged in to a hub as a swimmer (swim auth login URL --access read|readwrite)")
		return nil
	}
	fmt.Printf("hub:     %s\nswimmer: %s (%s, id %s)\naccess:  %s (swim auth login %s --access read|readwrite to change it)\n", a.URL, a.Name, a.User, a.ID, a.Access, a.URL)
	st, err := authClient(a).Machine(a.Token)
	if err != nil {
		fmt.Printf("state:   unknown: %s\n", hub.Explain(err))
		return nil
	}
	if st.Pending {
		fmt.Printf("state:   waiting for the hub's admin to admit it (swim auth admit %s)\n", a.Name)
	} else {
		fmt.Println("state:   admitted")
	}
	fmt.Printf("directories through it (%d):\n", len(st.Workers))
	for _, w := range st.Workers {
		fmt.Println("  " + w)
	}
	return nil
}

func authClient(a *authFile) *hub.Client {
	c, err := hub.NewClientTLS(a.URL, "", "", []byte(a.CA))
	if err != nil {
		return hub.NewClient(a.URL, "", "")
	}
	return c
}

func authLogout(args []string) error {
	var admin bool
	rest, err := flags{bools: map[string]*bool{"admin": &admin}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usagef("usage: swim auth logout [--admin]   (--admin: forget the admin login too)")
	}
	if admin {
		if err := os.Remove(hubLoginPath()); err == nil {
			fmt.Printf("forgot the admin login (%s)\n", hubLoginPath())
		}
	}
	a, err := loadAuth()
	if err != nil {
		fmt.Println("not logged in to a hub as a swimmer")
		return nil
	}
	n, err := authClient(a).LeaveMachine(a.Token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "swim auth: %s (logged out here anyway; the hub's admin can remove %s)\n", hub.Explain(err), a.Name)
	}
	os.Remove(authPath())
	// The directories that joined through it forget it too.
	dirs := 0
	for _, e := range runnerEntries() {
		if h, err := readHub(e.Root); err == nil && h.Via == viaAuth {
			os.Remove(hubPath(e.Root))
			history.Log(e.Root, history.Entry{Event: history.Hub, Detail: "left " + h.URL + " (swim auth logout)"})
			dirs++
		}
	}
	fmt.Printf("logged out of %s: the hub removed this machine and %d directories that joined through it; %d here forgot it\n", a.URL, n, dirs)
	fmt.Println("headless swimmers running now stop reporting at once; stop them with swim stop --all")
	return nil
}

// viaAuth marks a directory's .swim/hub.yml made from swim auth.
const viaAuth = "auth"

// ensureHub enrolls root through swim auth when it has no enrollment of
// its own and this machine is logged in to a hub; it returns root's
// enrollment, if any. swim.yml's hub: {name, namespace} names it.
func ensureHub(root string) (*hubFile, error) {
	if h, err := loadHub(root); err == nil {
		return h, nil
	}
	a, err := loadAuth()
	if err != nil {
		return nil, fmt.Errorf("this machine isn't logged in to a hub (swim auth login URL --access read|readwrite): %w", os.ErrNotExist)
	}
	name, ns := "", ""
	if cfg, err := loadCfg(root); err == nil {
		name = cfg.HubName()
		if v := cfg.HubNamespace(); v != "" {
			ns = hub.CleanNamespace(v)
		}
	}
	if ns == "" {
		ns = enrollNamespace(root, a.URL)
	}
	w := hubWorker(root, name, ns, a.Access)
	c := authClient(a)
	c.Token = a.Token
	id, err := c.Enroll(w)
	if err != nil {
		return nil, fmt.Errorf("enrolling through swim auth with %s: %w", a.URL, err)
	}
	h := &hubFile{URL: c.URL, WorkerToken: c.WorkerToken, ID: id, Name: w.Name, Namespace: ns, Access: a.Access, CA: a.CA, Via: viaAuth}
	if err := saveHub(root, h); err != nil {
		return nil, err
	}
	history.Log(root, history.Entry{Event: history.Hub, Detail: "enrolled with " + c.URL + " as " + w.Name + " through swim auth (machine " + a.Name + ")"})
	fmt.Printf("swim: joined %s to %s as %s through swim auth (machine %s)\n", tildePath(root), c.URL, w.Name, a.Name)
	if w.Key != "" {
		fmt.Printf("swim: its key for secrets: %s (the hub's admin approves it: a on its row, or swim auth approve %s)\n", hub.KeyFingerprint(w.Key), w.Name)
	}
	return h, nil
}
