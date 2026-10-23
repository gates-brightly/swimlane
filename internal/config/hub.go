package config

import (
	"fmt"
	"strings"
)

// HubSettings is swim.yml's hub: block: what this repo's workers (swim
// watch, enrolled with a hub) send it beyond their snapshots. Only the
// committed swim.yml may set it, as for headless:, so sending more takes a
// commit, not hub access.
type HubSettings struct {
	// Logs is how much lane output leaves this machine for the hub:
	// request (the default: a log only when a hub user asks a headless
	// worker for it), stream (also each round's log as it grows, and each
	// finished run's step output), or off (none). Finished runs go either
	// way, with their steps, commands, exits and durations; their output
	// only under stream.
	Logs string `yaml:"logs,omitempty"`
	// Name is what this directory is called on the hub when it joins
	// through swim auth (default: the directory's name).
	Name string `yaml:"name,omitempty"`
	// Namespace is the hub tab it shows under (default: its origin
	// remote's, e.g. github.com/org/api).
	Namespace string `yaml:"namespace,omitempty"`
}

// HubName is hub.name ("" for the default).
func (c *Config) HubName() string {
	if c.Hub == nil {
		return ""
	}
	return strings.TrimSpace(c.Hub.Name)
}

// HubNamespace is hub.namespace ("" for the default).
func (c *Config) HubNamespace() string {
	if c.Hub == nil {
		return ""
	}
	return strings.TrimSpace(c.Hub.Namespace)
}

// hub.logs values.
const (
	HubLogsRequest = "request"
	HubLogsStream  = "stream"
	HubLogsOff     = "off"
)

func (h *HubSettings) validate() error {
	if h == nil {
		return nil
	}
	switch strings.ToLower(h.Logs) {
	case "", HubLogsRequest, HubLogsStream, HubLogsOff:
		return nil
	}
	return fmt.Errorf("hub.logs must be request, stream or off, got %q", h.Logs)
}

// HubLogs is hub.logs: request (the default), stream or off; off as well
// when headless.logs is off.
func (c *Config) HubLogs() string {
	if c.Headless != nil {
		switch strings.ToLower(c.Headless.Logs) {
		case "off", "false":
			return HubLogsOff
		}
	}
	if c.Hub == nil || c.Hub.Logs == "" {
		return HubLogsRequest
	}
	return strings.ToLower(c.Hub.Logs)
}
