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
