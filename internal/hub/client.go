package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a worker's connection to a hub.
type Client struct {
	URL         string
	Token       string // the enroll token (enrolls again after a hub forgot us)
	WorkerToken string // this worker's own token, from enrolling
	ID          string
	HTTP        *http.Client
}

// NewClient makes a client with a short timeout: a slow hub never holds
// up a worker.
func NewClient(url, token, id string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, ID: id, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// own is the token for this worker's own calls.
func (c *Client) own() string {
	if c.WorkerToken != "" {
		return c.WorkerToken
	}
	return c.Token
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	return c.doAs(c.own(), method, path, body)
}

func (c *Client) doAs(token, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.URL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
}

func failure(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("hub: %s: %s", resp.Status, strings.TrimSpace(string(b)))
}

// Enroll joins the hub (with the enroll token, else our own) and returns
// the worker's id; its own token is in c.WorkerToken after.
func (c *Client) Enroll(w Worker) (string, error) {
	tok := c.Token
	if tok == "" {
		tok = c.WorkerToken
	}
	resp, err := c.doAs(tok, "POST", "/api/enroll", w)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", failure(resp)
	}
	var out struct{ ID, Token string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ID == "" {
		return "", errors.New("hub: enroll returned no id")
	}
	c.ID = out.ID
	if out.Token != "" {
		c.WorkerToken = out.Token
	}
	return out.ID, nil
}

// FetchGrants gets the secrets granted to this worker, still encrypted to
// its key.
func (c *Client) FetchGrants() ([]Fetched, error) {
	resp, err := c.do("GET", "/api/grants/"+c.ID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, failure(resp)
	}
	var out struct{ Grants []Fetched }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("hub: grants: %w", err)
	}
	return out.Grants, nil
}

// Admin is the operator's connection to a hub, with its admin token.
type Admin struct{ c *Client }

// NewAdmin makes an admin client.
func NewAdmin(url, adminToken string) *Admin {
	c := NewClient(url, adminToken, "")
	c.HTTP.Timeout = 10 * time.Second
	return &Admin{c: c}
}

func (a *Admin) call(method, path string, body, out any) error {
	resp, err := a.c.do(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return failure(resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Workers lists the hub's workers.
func (a *Admin) Workers() ([]View, error) {
	var out struct{ Workers []View }
	return out.Workers, a.call("GET", "/api/workers", nil, &out)
}

// WorkerKey is a worker's public key (to encrypt a grant to), its
// fingerprint, and whether it's approved.
func (a *Admin) WorkerKey(id string) (key, fp string, approved bool, err error) {
	var out struct {
		Key      string `json:"key"`
		KeyFP    string `json:"key_fp"`
		Approved bool   `json:"approved"`
	}
	err = a.call("GET", "/api/workers/"+id+"/key", nil, &out)
	return out.Key, out.KeyFP, out.Approved, err
}

// Approve approves a worker for secrets (or withdraws it, with off).
func (a *Admin) Approve(id string, off bool) (string, error) {
	var out struct{ Message string }
	method := "POST"
	if off {
		method = "DELETE"
	}
	err := a.call(method, "/api/workers/"+id+"/approve", nil, &out)
	return out.Message, err
}

// Grant stores a secret encrypted to worker id's key.
func (a *Admin) Grant(id, name, ciphertext, keyFP, by string) error {
	return a.call("PUT", "/api/grants/"+id+"/"+name, Grant{Ciphertext: ciphertext, KeyFP: keyFP, By: by}, nil)
}

// Revoke deletes a grant.
func (a *Admin) Revoke(id, name string) error {
	return a.call("DELETE", "/api/grants/"+id+"/"+name, nil, nil)
}

// Grants lists every grant (names, never values).
func (a *Admin) Grants() ([]GrantInfo, error) {
	var out struct{ Grants []GrantInfo }
	return out.Grants, a.call("GET", "/api/grants", nil, &out)
}

// Report sends a snapshot; a hub that doesn't know the worker (restarted)
// gets it enrolled again first.
func (c *Client) Report(s Snapshot) error {
	resp, err := c.do("POST", "/api/report/"+c.ID, s)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		if _, err := c.Enroll(s.Worker); err != nil {
			return err
		}
		resp, err = c.do("POST", "/api/report/"+c.ID, s)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return nil
		}
	}
	return failure(resp)
}

// Leave removes the worker from the hub.
func (c *Client) Leave() error {
	resp, err := c.do("DELETE", "/api/workers/"+c.ID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return failure(resp)
	}
	return nil
}
