// Package authority decides whether a guard is approved by what swim can
// check without asking anyone: under swim run, a guard flag set in the
// environment swim run started with. It also records approvals in the
// chain with the runner's key.
//
// Under swim run the launcher decides, from a Snapshot it takes when the
// run starts: what a lane sets in its own environment after that doesn't
// count.
package authority

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// The runner's signing key, loaded once per process.
var (
	keyOnce sync.Once
	keyVal  *sign.Key
	keyErr  error
)

// Key is the key this swim signs chain entries with: nil (and no error)
// when signing is off, or auto and no key loads.
func Key(cfg *config.Config) (*sign.Key, error) {
	mode := cfg.SigningMode()
	if mode == config.SigningOff {
		return nil, nil
	}
	keyOnce.Do(func() {
		if !sign.Available() {
			keyErr = fmt.Errorf("ssh-keygen isn't installed")
			return
		}
		keyVal, keyErr = sign.LoadKey(cfg.SigningKey)
	})
	if keyErr != nil {
		if mode == config.SigningRequired {
			return nil, fmt.Errorf("signing: required, but %v", keyErr)
		}
		return nil, nil
	}
	return keyVal, nil
}

// Record appends an entry to the chain, signed by the runner's key when
// there is one, and returns its hash. The chain is best-effort for the
// round (a failure is reported, not fatal) unless signing is required.
func Record(root string, cfg *config.Config, e chain.Entry) (string, error) {
	key, err := Key(cfg)
	if err != nil {
		return "", err
	}
	return RecordWith(root, cfg, key, e)
}

// RecordWith is Record signed with key (swim run's run key) instead of
// the runner's.
func RecordWith(root string, cfg *config.Config, key *sign.Key, e chain.Entry) (string, error) {
	hash, err := chain.Append(root, e, key)
	if errors.As(err, new(chain.SignError)) && cfg.SigningMode() != config.SigningRequired {
		// signing: auto and the key couldn't sign (a passphrase and no
		// agent, a hardware key that wants a PIN): keep the link, unsigned.
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: couldn't sign ("+err.Error()+"); recorded unsigned")
		hash, err = chain.Append(root, e, nil)
	}
	if err != nil {
		if cfg.SigningMode() == config.SigningRequired {
			return "", err
		}
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: "+err.Error())
	}
	return hash, nil
}

// Snapshot is what approves guards without a question, as it stood when
// it was taken.
type Snapshot struct {
	// Env is the environment: guard flags set to 1, and a queued batch's
	// SWIM_QUEUE and SWIM_QUEUE_APPROVED.
	Env map[string]string
}

// Take snapshots env.
func Take(env []string) *Snapshot {
	s := &Snapshot{Env: map[string]string{}}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			s.Env[k] = v
		}
	}
	return s
}

// Result is a decision.
type Result struct {
	OK bool
	// How is the APPROVED line's detail ("FLAG=1").
	How string
	// Entry is the approve entry to record (OK only): method and
	// approvers; the caller adds the lane's facts.
	Entry chain.Entry
	// History is what .swim.log says of it (OK only).
	History string
	// Why it isn't approved.
	Why string
}

// FlagSet decides a guard whose flag the lane has set to 1: it approves
// only when the snapshot's environment (swim run's own) set it too, or the
// queued batch approved it.
func (s *Snapshot) FlagSet(flag string) Result {
	if s.Env[flag] != "1" {
		return Result{Why: flag + "=1 was set inside the lane, not in the environment swim run started with"}
	}
	how, method := flag+"=1", "flag"
	if strings.Contains(" "+s.Env["SWIM_QUEUE_APPROVED"]+" ", " "+flag+" ") {
		how, method = how+", approved in "+s.Env["SWIM_QUEUE"], "queue"
	}
	return Result{OK: true, How: how, Entry: chain.Entry{Method: method, Statement: how}}
}

// GuardState is how swim run would decide one guard as it starts, from
// what the snapshot holds: what swim status shows for a pending lane.
type GuardState struct {
	Flag     string `yaml:"flag"`
	Desc     string `yaml:"description,omitempty"`
	Why      string `yaml:"why,omitempty"` // why the change is made (the header's why: line)
	Approved bool   `yaml:"approved"`
	// How it's approved ("FLAG=1").
	How string `yaml:"how,omitempty"`
	// Asks: not approved, so swim run would ask at its prompt (a dry run
	// if no one can answer).
	Asks bool `yaml:"asks"`
}

// Guard decides flag as swim run would as it starts: approved by the flag
// in the environment swim run starts with, or not (swim run asks the
// operator about one still unapproved).
func (s *Snapshot) Guard(flag, desc string) GuardState {
	g := GuardState{Flag: flag, Desc: desc}
	if s.Env[flag] == "1" {
		g.Approved, g.How = true, s.FlagSet(flag).How
		return g
	}
	g.Asks = true
	return g
}

// Text is the guard's state in a line: "approved: FLAG=1", or "not
// approved; swim run will ask (a dry run if no one answers)".
func (g GuardState) Text() string {
	if g.Approved {
		return "approved: " + g.How
	}
	return "not approved; swim run will ask (a dry run if no one answers)"
}
