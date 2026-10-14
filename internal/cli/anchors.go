package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/anchor"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/sign"
)

// Checkpoints: at the end of every top-level swim run, swim all, swim ci
// and swim watch batch (and on swim chain checkpoint), swim signs the
// chain's head and sends it to each chain_anchors destination, so entries
// removed from the end of the chain later, or a chain replaced wholesale,
// are caught by swim verify's anchored level. A failed send is a warning;
// the next checkpoint is sent anyway, and swim doctor lists anchors that
// haven't had the latest one.

// checkpointsLog is this repo's own record of its checkpoints (a lane can
// rewrite it; the anchors are what count).
func checkpointsLog(root string) string { return filepath.Join(root, ".swim", "checkpoints.jsonl") }

// sentPath records the last checkpoint seq each anchor took.
func sentPath(root string) string { return filepath.Join(root, ".swim", "checkpoints.sent.yml") }

// openAnchor opens one anchor spec for root.
func openAnchor(root, spec string) (anchor.Anchor, error) {
	return anchor.Open(spec, anchor.Options{Root: root, Hub: func() (*hub.Client, error) {
		h, err := loadHub(root)
		if err != nil {
			return nil, errors.New("this directory isn't enrolled with a hub (swim hub enroll)")
		}
		return hubClient(h), nil
	}})
}

// takeCheckpoint signs the chain's head and sends it to the configured
// anchors (and to extra), warning about each that fails. Nothing is taken
// inside a lane (a nested swim run), with no anchors and no extra unless
// always, or for an empty chain. sent holds the anchors that took this
// checkpoint: an anchor that failed is dropped from the record of sends
// (swim doctor lists it until one gets through).
func takeCheckpoint(root string, cfg *config.Config, always bool, extra ...string) (cp *chain.Checkpoint, sent map[string]bool, err error) {
	if os.Getenv("SWIM_LANE") != "" {
		return nil, nil, nil
	}
	specs := append(append([]string(nil), cfg.ChainAnchors...), extra...)
	if len(specs) == 0 && !always {
		return nil, nil, nil
	}
	links, err := chain.Read(root)
	if err != nil {
		return nil, nil, err
	}
	if len(links) == 0 {
		return nil, nil, nil
	}
	host, _ := os.Hostname()
	c := chain.NewCheckpoint(links, host)
	cp = &c
	key, err := runnerKey(cfg)
	if err != nil {
		return nil, nil, err
	}
	if key != nil {
		if err := chain.SignCheckpoint(cp, key); err != nil {
			if cfg.SigningMode() == config.SigningRequired {
				return nil, nil, err
			}
			fmt.Fprintln(os.Stderr, "swim: warning: chain checkpoint: couldn't sign ("+err.Error()+"); sending it unsigned")
			cp.Signer, cp.Sig, cp.RunnerKeyFP = "", "", ""
		}
	}
	if line, err := json.Marshal(cp); err == nil {
		if f, err := os.OpenFile(checkpointsLog(root), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.Write(append(line, '\n'))
			f.Close()
		}
	}
	record := loadSent(root)
	sent = map[string]bool{}
	for _, spec := range specs {
		if err := sendCheckpoint(root, spec, *cp); err != nil {
			fmt.Fprintf(os.Stderr, "swim: warning: chain anchor %s: %v (the next checkpoint is sent anyway; swim doctor lists it until then)\n", spec, err)
			delete(record, spec)
			continue
		}
		record[spec] = cp.Seq
		sent[spec] = true
	}
	saveSent(root, record)
	return cp, sent, nil
}

func sendCheckpoint(root, spec string, cp chain.Checkpoint) error {
	a, err := openAnchor(root, spec)
	if err != nil {
		return err
	}
	return a.PutCheckpoint(cp)
}

// checkpointAfterRun is takeCheckpoint at the end of a run, batch or CI
// job: never its result, only warnings.
func checkpointAfterRun(root string, cfg *config.Config) {
	if _, _, err := takeCheckpoint(root, cfg, false); err != nil {
		fmt.Fprintln(os.Stderr, "swim: warning: chain checkpoint: "+err.Error())
	}
}

func loadSent(root string) map[string]int {
	m := map[string]int{}
	if data, err := os.ReadFile(sentPath(root)); err == nil {
		yaml.Unmarshal(data, &m)
	}
	return m
}

func saveSent(root string, m map[string]int) {
	if data, err := yaml.Marshal(m); err == nil {
		os.WriteFile(sentPath(root), data, 0o644)
	}
}

// latestCheckpoint reads spec's newest checkpoint and checks it: a bad
// signature, or a signer trusted doesn't vouch for (when trust.yml is in
// use), and it's ignored with a warning.
func (v *verifier) latestCheckpoint(spec string) (*chain.Checkpoint, error) {
	a, err := openAnchor(v.root, spec)
	if err != nil {
		return nil, err
	}
	cp, err := a.Latest()
	if err != nil || cp == nil {
		return nil, err
	}
	switch err := chain.VerifyCheckpoint(*cp); {
	case errors.Is(err, chain.ErrUnsigned) && v.trust == nil:
		v.warn = append(v.warn, fmt.Sprintf("anchor %s: checkpoint %s is unsigned", spec, cp))
	case err != nil:
		v.warn = append(v.warn, fmt.Sprintf("anchor %s: checkpoint %s ignored: %v", spec, cp, err))
		return nil, nil
	case v.trust != nil && !v.trust(cp.Signer):
		v.warn = append(v.warn, fmt.Sprintf("anchor %s: checkpoint %s ignored: signed by %s, a key not in trust.yml", spec, cp, sign.Fingerprint(cp.Signer)))
		return nil, nil
	}
	return cp, nil
}
