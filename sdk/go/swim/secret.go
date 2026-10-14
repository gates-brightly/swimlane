package swim

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Secret gets secrets stored with swim secret set, by name:
//
//	password, err := swim.Secret.Get("db_password")
//
// Inside a swim run it asks the run's secrets broker (the run decrypted
// them at start, and masks every one as ***(name) in what swim writes);
// outside one it runs swim secret get, which decrypts with your key.
var Secret secrets

type secrets struct{}

// Get returns the secret called name.
func (secrets) Get(name string) (string, error) {
	path := os.Getenv("SWIM_SECRETS")
	if path == "" {
		bin := os.Getenv("SWIM_BIN")
		if bin == "" {
			bin = "swim"
		}
		cmd := exec.Command(bin, "secret", "get", name)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return "", errors.New(msg)
			}
			return "", fmt.Errorf("swim secret get %s: %w", name, err)
		}
		return string(out), nil
	}
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("secret %s: the run's secrets broker: %w", name, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	lane, _ := strconv.Atoi(os.Getenv("SWIM_LANE"))
	req := map[string]any{"token": os.Getenv("SWIM_SECRETS_TOKEN"), "op": "get", "name": name, "lane": lane, "how": "sdk"}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return "", err
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&resp); err != nil {
		return "", fmt.Errorf("secret %s: %w", name, err)
	}
	if !resp.OK {
		return "", fmt.Errorf("secret %s: %s", name, resp.Error)
	}
	return resp.Value, nil
}
