package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The worker's side of commands (Poll, Ack, UploadLog), the admin's of
// users (AddUser, RemoveUser, Users), and a user's (Send, Workers, Log).

// Poll gets this worker's commands after seq after, waiting up to wait for
// one (0: answer at once). epoch is the hub epoch after counts in; the
// reply names the hub's.
func (c *Client) Poll(ctx context.Context, after int, epoch string, wait time.Duration) (string, []Command, error) {
	q := url.Values{"after": {strconv.Itoa(after)}, "epoch": {epoch}, "wait": {wait.String()}}
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL+"/api/workers/"+url.PathEscape(c.ID)+"/commands?"+q.Encode(), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.own())
	// The long poll outlives the client's short timeout.
	hc := *c.HTTP
	hc.Timeout = wait + 10*time.Second
	resp, err := hc.Do(req)
	if err != nil {
		return "", nil, Explain(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, failure(resp)
	}
	var out struct {
		Epoch    string    `json:"epoch"`
		Commands []Command `json:"commands"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil, fmt.Errorf("hub: commands: %w", err)
	}
	return out.Epoch, out.Commands, nil
}

// Ack tells the hub what became of a command.
func (c *Client) Ack(cid, outcome, detail string) error {
	resp, err := c.do("POST", "/api/workers/"+url.PathEscape(c.ID)+"/commands/"+url.PathEscape(cid), Ack{Outcome: outcome, Detail: detail})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return failure(resp)
	}
	return nil
}

// PutRun sends a finished run. A refusal that sending it again won't
// change (a bad or oversized report) is permanent.
func (c *Client) PutRun(rep *RunReport) (permanent bool, err error) {
	data, err := json.Marshal(rep)
	if err != nil {
		return true, err
	}
	req, err := http.NewRequest("PUT", c.URL+"/api/workers/"+url.PathEscape(c.ID)+"/runs/"+url.PathEscape(rep.Run.Run), bytes.NewReader(data))
	if err != nil {
		return true, err
	}
	req.Header.Set("Authorization", "Bearer "+c.own())
	req.Header.Set("Content-Type", "application/json")
	hc := *c.HTTP
	hc.Timeout = time.Minute
	resp, err := hc.Do(req)
	if err != nil {
		return false, Explain(err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return false, nil
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return true, failure(resp)
	}
	return false, failure(resp)
}

// UploadLog sends the log a log command asked for (at most LogMax).
func (c *Client) UploadLog(run string, lane int, data []byte) error {
	req, err := http.NewRequest("PUT", c.URL+"/api/workers/"+url.PathEscape(c.ID)+"/logs/"+url.PathEscape(run)+"/"+strconv.Itoa(lane), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.own())
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	hc := *c.HTTP
	hc.Timeout = time.Minute
	resp, err := hc.Do(req)
	if err != nil {
		return Explain(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return failure(resp)
	}
	return nil
}

// AddUser adds (or replaces) a hub user and returns their token.
func (a *Admin) AddUser(name, role string) (string, error) {
	var out struct{ Token string }
	err := a.call("POST", "/api/users", UserInfo{Name: name, Role: role}, &out)
	return out.Token, err
}

// RemoveUser removes a hub user.
func (a *Admin) RemoveUser(name string) error {
	return a.call("DELETE", "/api/users/"+url.PathEscape(name), nil, nil)
}

// Users lists the hub's users.
func (a *Admin) Users() ([]UserInfo, error) {
	var out struct{ Users []UserInfo }
	return out.Users, a.call("GET", "/api/users", nil, &out)
}

// UserClient is a hub user's connection, with their own token.
type UserClient struct{ a *Admin }

// NewUser makes a user client trusting caPEM for https, as NewClientTLS.
func NewUser(url, token string, caPEM []byte) (*UserClient, error) {
	a, err := NewAdminTLS(url, token, caPEM)
	if err != nil {
		return nil, err
	}
	return &UserClient{a: a}, nil
}

// Send sends worker id a command as this user.
func (u *UserClient) Send(id string, c Command) (Command, error) {
	var out Command
	err := u.a.call("POST", "/api/workers/"+url.PathEscape(id)+"/commands", c, &out)
	return out, err
}

// Workers lists the hub's workers (with the questions each waits on).
func (u *UserClient) Workers() ([]View, error) { return u.a.Workers() }

// Log reads a log a command had uploaded (an error until it is).
func (u *UserClient) Log(id, run string, lane int) (string, error) {
	resp, err := u.a.c.do("GET", "/api/enrollments/"+url.PathEscape(id)+"/runs/"+url.PathEscape(run)+"/jobs/"+strconv.Itoa(lane)+"/log", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", failure(resp)
	}
	var b bytes.Buffer
	_, err = b.ReadFrom(resp.Body)
	return b.String(), err
}
