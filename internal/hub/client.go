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
	URL   string
	Token string
	ID    string
	HTTP  *http.Client
}

// NewClient makes a client with a short timeout: a slow hub never holds
// up a worker.
func NewClient(url, token, id string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Token: token, ID: id, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
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
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	return c.HTTP.Do(req)
}

func failure(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("hub: %s: %s", resp.Status, strings.TrimSpace(string(b)))
}

// Enroll joins the hub and returns the worker's id.
func (c *Client) Enroll(w Worker) (string, error) {
	resp, err := c.do("POST", "/api/enroll", w)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", failure(resp)
	}
	var out struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ID == "" {
		return "", errors.New("hub: enroll returned no id")
	}
	c.ID = out.ID
	return out.ID, nil
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
