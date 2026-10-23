package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Headless is swim.yml's headless: block: what a headless worker (swim
// watch --headless) in this repo accepts from its hub. Only the committed
// swim.yml may set it, so changing it takes a commit, not hub access.
type Headless struct {
	// Approvals is how a guard waiting under --headless may be approved
	// through the hub: signed (the default: an approver's SSH signature) or
	// hub (also a button click by a hub user listed in lookup.yml, for
	// guards no require: rule names).
	Approvals string `yaml:"approvals,omitempty"`
	// Allow are the hub commands this repo accepts (default
	// HeadlessDefaultAllow; run must be listed to be allowed).
	Allow []string `yaml:"allow,omitempty"`
	// Logs is on (the default) or off: off refuses every log request, for
	// repos whose logs must not leave the box.
	Logs string `yaml:"logs,omitempty"`
	// CommandTTL caps how long after it was sent a hub command may still be
	// carried out (default 10m; interrupt is capped at 2m too).
	CommandTTL string `yaml:"command_ttl,omitempty"`
	// AnswerTimeout is how long a guard waits for an answer through the hub
	// (default 30m); no answer by then is a decline.
	AnswerTimeout string `yaml:"answer_timeout,omitempty"`
}

// Hub commands a headless worker knows.
const (
	HubAnswer    = "answer"
	HubInterrupt = "interrupt"
	HubDrain     = "drain"
	HubRun       = "run"
	HubLog       = "log"
	HubTrash     = "trash"
	HubPause     = "pause"
	HubUnpause   = "unpause"
	HubQueue     = "queue"   // queue lanes as one batch (unsigned, no approvals)
	HubDequeue   = "dequeue" // take a batch off the queue
	HubRequeue   = "requeue" // queue a finished batch's lanes again (unsigned, no approvals)
)

// HubCommands are every command kind, in the order docs list them.
var HubCommands = []string{HubAnswer, HubInterrupt, HubDrain, HubRun, HubLog, HubTrash, HubPause, HubUnpause, HubQueue, HubDequeue, HubRequeue}

// HeadlessDefaultAllow is headless.allow when swim.yml doesn't set it:
// the commands that only stop, watch or answer; the ones that queue, run,
// remove or change work (run, trash, pause, unpause, queue, dequeue,
// requeue) must be enabled on purpose.
var HeadlessDefaultAllow = []string{HubAnswer, HubInterrupt, HubDrain, HubLog}

// Headless approval modes.
const (
	HeadlessSigned = "signed"
	HeadlessHub    = "hub"
)

// Command lifetimes: a command is never carried out later than this after
// it was sent; interrupt never later than InterruptTTL.
const (
	DefaultCommandTTL    = 10 * time.Minute
	InterruptTTL         = 2 * time.Minute
	DefaultAnswerTimeout = 30 * time.Minute
)

func (h *Headless) validate() error {
	if h == nil {
		return nil
	}
	switch h.Approvals {
	case "", HeadlessSigned, HeadlessHub:
	default:
		return fmt.Errorf("headless.approvals must be signed or hub, got %q", h.Approvals)
	}
	for _, a := range h.Allow {
		if !slices.Contains(HubCommands, a) {
			return fmt.Errorf("headless.allow: unknown command %q (known: %s)", a, strings.Join(HubCommands, ", "))
		}
	}
	switch strings.ToLower(h.Logs) {
	case "", "on", "off", "true", "false":
	default:
		return fmt.Errorf("headless.logs must be on or off, got %q", h.Logs)
	}
	for _, kv := range [][2]string{{"command_ttl", h.CommandTTL}, {"answer_timeout", h.AnswerTimeout}} {
		if kv[1] == "" {
			continue
		}
		if d, err := time.ParseDuration(kv[1]); err != nil || d <= 0 {
			return fmt.Errorf("headless.%s must be a duration like 10m, got %q", kv[0], kv[1])
		}
	}
	return nil
}

// HeadlessAllow is the hub commands this repo accepts.
func (c *Config) HeadlessAllow() []string {
	if c.Headless == nil || c.Headless.Allow == nil {
		return HeadlessDefaultAllow
	}
	return c.Headless.Allow
}

// HeadlessAllows reports whether the repo accepts hub command kind.
func (c *Config) HeadlessAllows(kind string) bool { return slices.Contains(c.HeadlessAllow(), kind) }

// HeadlessLogs reports whether a headless worker may upload logs to its hub
// (headless.logs, default on; hub.logs: off turns it off too).
func (c *Config) HeadlessLogs() bool {
	if c.Hub != nil && strings.EqualFold(c.Hub.Logs, HubLogsOff) {
		return false
	}
	if c.Headless == nil {
		return true
	}
	switch strings.ToLower(c.Headless.Logs) {
	case "off", "false":
		return false
	}
	return true
}

// HeadlessApprovals is headless.approvals: signed (the default) or hub.
func (c *Config) HeadlessApprovals() string {
	if c.Headless == nil || c.Headless.Approvals == "" {
		return HeadlessSigned
	}
	return c.Headless.Approvals
}

// CommandTTL is how long after it was sent a hub command of kind may still
// be carried out: headless.command_ttl (default 10m), and at most 2m for
// interrupt.
func (c *Config) CommandTTL(kind string) time.Duration {
	ttl := DefaultCommandTTL
	if c.Headless != nil {
		if d, err := time.ParseDuration(c.Headless.CommandTTL); err == nil && d > 0 {
			ttl = d
		}
	}
	if kind == HubInterrupt && ttl > InterruptTTL {
		ttl = InterruptTTL
	}
	return ttl
}

// AnswerTimeout is headless.answer_timeout (default 30m).
func (c *Config) AnswerTimeout() time.Duration {
	if c.Headless != nil {
		if d, err := time.ParseDuration(c.Headless.AnswerTimeout); err == nil && d > 0 {
			return d
		}
	}
	return DefaultAnswerTimeout
}

// Hub access: what an enrollment (swim hub enroll --access, kept in
// .swim/hub.yml) lets its hub do, chosen once per enrollment rather than
// per repo. read: the hub shows the worker and fetches its logs, and
// nothing else (no commands, no answers). readwrite: every hub command.
// A committed swim.yml headless.allow still narrows either; "" (an
// enrollment from before access levels) is headless.allow as it was.
const (
	AccessRead      = "read"
	AccessReadWrite = "readwrite"
)

// ValidAccess: read or readwrite.
func ValidAccess(a string) bool { return a == AccessRead || a == AccessReadWrite }

// AccessGrants is the hub commands access lets through ("" for an
// enrollment from before access levels: nil, see HubAllow).
func AccessGrants(access string) []string {
	switch access {
	case AccessRead:
		return []string{HubLog}
	case AccessReadWrite:
		return HubCommands
	}
	return nil
}

// HubAllow is the hub commands a headless worker takes under access: what
// access grants, narrowed to swim.yml's headless.allow when swim.yml sets
// one. An enrollment from before access levels ("") takes HeadlessAllow.
func (c *Config) HubAllow(access string) []string {
	grants := AccessGrants(access)
	if grants == nil {
		return c.HeadlessAllow()
	}
	if c.Headless == nil || c.Headless.Allow == nil {
		return grants
	}
	var out []string
	for _, k := range grants {
		if slices.Contains(c.Headless.Allow, k) {
			out = append(out, k)
		}
	}
	return out
}

// HubAllows reports whether a headless worker takes hub command kind
// under access.
func (c *Config) HubAllows(access, kind string) bool {
	return slices.Contains(c.HubAllow(access), kind)
}
