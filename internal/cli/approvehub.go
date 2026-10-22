package cli

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// approveHub answers a guard waiting on a headless worker, from wherever
// the approver is: it fetches the question from the hub, shows it, signs
// the statement (approval.HubQuestion) with the approver's SSH key and
// sends the answer as this hub user. The worker, not the hub, checks the
// signature against its lookup.yml.
//
//	swim approve --hub WORKER/QUESTION [--decline] [--key K] [--url URL] [--token T]
func approveHub(ref, keyPath, url, token string, decline bool) error {
	worker, qid, ok := strings.Cut(ref, "/")
	if !ok || worker == "" || qid == "" {
		return usagef("usage: swim approve --hub WORKER/QUESTION [--decline] [--key K] [--url URL] [--token T]   (both are in the hub's Approvals inbox)")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	u, url, err := hubUserClient(root, url, token)
	if err != nil {
		return err
	}
	w, err := hubWorkerRef(u, worker)
	if err != nil {
		return err
	}
	var p *hub.PendingApproval
	var ids []string
	for i := range w.Waiting {
		if q := &w.Waiting[i]; q.ID == qid || len(qid) >= 6 && strings.HasPrefix(q.ID, qid) {
			if p != nil {
				return fmt.Errorf("%s matches more than one question on %s: give more of it", qid, w.Name)
			}
			p = q
		}
		ids = append(ids, w.Waiting[i].ID)
	}
	if p == nil {
		return fmt.Errorf("no question %s waits on %s (answered, timed out, or never asked); waiting: %s", qid, w.Name, or(strings.Join(ids, ", "), "none"))
	}
	decision := approval.Approve
	if decline {
		decision = approval.Decline
	}
	pp := painter()
	fmt.Println(pp.Paint(ui.Bold, fmt.Sprintf("%s (%s) · swim %d asks to approve %s", w.Name, w.ID, p.Lane, p.Flag)))
	fmt.Println("  the lane says: " + p.Desc)
	if p.Evidence != "" {
		fmt.Println("evidence (what it will do):")
		for _, l := range strings.Split(strings.TrimRight(p.Evidence, "\n"), "\n") {
			fmt.Println("  " + l)
		}
	}
	statement := p.Question(w.ID).Statement(decision)
	fmt.Println("signing:")
	for _, l := range strings.Split(strings.TrimRight(statement, "\n"), "\n") {
		fmt.Println("  " + l)
	}
	key, err := sign.LoadKey(or(keyPath, cfg.SigningKey))
	if err != nil {
		return err
	}
	sig, err := sign.Sign(key, []byte(statement))
	if err != nil {
		return err
	}
	cmd, err := u.Send(w.ID, hub.Command{Kind: hub.KindAnswer, Pin: hub.Pin{Request: p.ID, Evidence: p.EvidenceSHA},
		Answer: &hub.Answer{Decision: decision, Key: key.Public, Sig: sig}})
	if err != nil {
		return err
	}
	fmt.Printf("sent: %s %s's %s, signed with %s (command %s, seq %d)\n", decision, fmt.Sprintf("swim %d", p.Lane), p.Flag, keyComment(key.Public), cmd.CID, cmd.Seq)
	fmt.Printf("the worker checks it against its lookup.yml; its outcome shows on %s/e/%s/lanes\n", strings.TrimRight(url, "/"), w.ID)
	return nil
}

// hubUserClient is a hub user's client: the hub from --url, else
// SWIM_HUB_URL, swim hub login's or this repo's enrollment; the user's
// token from --token, else SWIM_HUB_USER_TOKEN (swim hub user add printed
// it); the CA from SWIM_HUB_CA, else the saved one for that hub.
func hubUserClient(root, url, token string) (*hub.UserClient, string, error) {
	url = or(url, os.Getenv("SWIM_HUB_URL"))
	token = or(token, os.Getenv("SWIM_HUB_USER_TOKEN"))
	var ca string
	if f := os.Getenv("SWIM_HUB_CA"); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, "", fmt.Errorf("SWIM_HUB_CA: %w", err)
		}
		ca = string(data)
	}
	saved := map[string]string{} // URL -> CA
	if data, err := os.ReadFile(hubLoginPath()); err == nil {
		var l hubLogin
		if yaml.Unmarshal(data, &l) == nil && l.URL != "" {
			url = or(url, l.URL)
			saved[l.URL] = l.CA
		}
	}
	if h, err := loadHub(root); err == nil {
		url = or(url, h.URL)
		saved[strings.TrimRight(h.URL, "/")] = h.CA
	}
	if ca == "" {
		ca = saved[strings.TrimRight(url, "/")]
	}
	switch {
	case url == "":
		return nil, "", fmt.Errorf("which hub? --url URL (or SWIM_HUB_URL)")
	case token == "":
		return nil, "", fmt.Errorf("no hub user token: --token T or SWIM_HUB_USER_TOKEN (the hub's admin gives it: swim hub user add NAME)")
	}
	if err := hub.CheckURL(url, false); err != nil {
		return nil, "", err
	}
	u, err := hub.NewUser(url, token, []byte(ca))
	if err != nil {
		return nil, "", fmt.Errorf("hub CA: %w", err)
	}
	return u, url, nil
}
