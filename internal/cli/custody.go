package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
)

// The runner's signing key, loaded once per process.
var (
	keyOnce sync.Once
	keyVal  *sign.Key
	keyErr  error
)

// runnerKey is the key this swim signs chain entries with: nil (and no
// error) when signing is off, or auto and no key loads.
func runnerKey(cfg *config.Config) (*sign.Key, error) {
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

// ScriptsDir keeps a copy of every lane script a round ran, by hash, so
// swim verify can show exactly what ran.
func scriptsDir(root string) string { return filepath.Join(root, ".swim", "scripts") }

// keepScript hashes lane n's script and keeps a copy under its hash: the
// staged copy the launcher pinned in m, or with none (a lane run directly
// with bash) the working tree's.
func keepScript(root string, n int, m *manifest.Manifest) (string, error) {
	var data []byte
	var err error
	if m != nil {
		data, err = m.ScriptBytes()
	} else {
		data, err = lane.ScriptBytes(root, n)
	}
	if err != nil {
		return "", err
	}
	sha := chain.Hash(data)
	dst := filepath.Join(scriptsDir(root), sha+".sh")
	if _, err := os.Stat(dst); err != nil {
		os.MkdirAll(scriptsDir(root), 0o755)
		tmp := fmt.Sprintf("%s.%d.tmp", dst, os.Getpid()) // a matrix's cells start at once
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return "", err
		}
	}
	return sha, nil
}

// scriptSHA is the hash of the script lane n's current round runs: from
// the manifest the launcher staged (SWIM_MANIFEST, read-only), else
// status.yml (recorded at the round's start), else the script on disk.
func scriptSHA(root string, n int) string {
	if m := manifest.FromEnv(n); m != nil {
		return m.ScriptSHA
	}
	if st, _ := status.Load(root); st != nil {
		if l := st.Mine(n); l != nil && l.ScriptSHA != "" {
			return l.ScriptSHA
		}
	}
	if data, err := lane.ScriptBytes(root, n); err == nil {
		return chain.Hash(data)
	}
	return ""
}

// laneManifest is the execution manifest lane n's round runs: the one the
// launcher staged, or built now from the working tree (a lane run directly
// with bash); nil if it can't be built.
func laneManifest(root string, cfg *config.Config, n int) *manifest.Manifest {
	if m := manifest.FromEnv(n); m != nil {
		return m
	}
	m, err := manifest.Build(root, cfg, n, "")
	if err != nil {
		return nil
	}
	return m
}

// manifestSHA is the manifest hash lane n's current round runs ("" for
// none): the launcher's, else the one status.yml recorded at its start.
func manifestSHA(root string, n int) string {
	if m := manifest.FromEnv(n); m != nil {
		return m.SHA256
	}
	if st, _ := status.Load(root); st != nil {
		if l := st.Mine(n); l != nil {
			return l.ManifestSHA
		}
	}
	return ""
}

// laneProbes are the Observe: probes of lane n's round (this cell's or
// job's): from the staged copy when the launcher staged one.
func laneProbes(root string, n int, m *manifest.Manifest) []lane.Probe {
	if m != nil && m.Dir != "" {
		data, err := os.ReadFile(m.Script())
		if err != nil {
			return nil
		}
		side := ""
		if m.Pinned.Sidecar != nil {
			side = m.Path(m.Pinned.Sidecar.Path)
		}
		return lane.ProbesIn(filepath.Base(m.Pinned.Script.Path), data, n, side, lane.CurrentCell())
	}
	info, _ := lane.ReadScript(root, n)
	return info.Observe
}

// record appends an entry to the chain, signed by the runner's key when
// there is one. The chain is best-effort for the round (a failure is
// reported, not fatal) unless signing is required.
func record(root string, cfg *config.Config, e chain.Entry) error {
	key, err := runnerKey(cfg)
	if err != nil {
		return err
	}
	_, err = chain.Append(root, e, key)
	if err != nil && key != nil && cfg.SigningMode() != config.SigningRequired {
		// signing: auto and the key couldn't sign (a passphrase and no
		// agent, a hardware key that wants a PIN): keep the link, unsigned.
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: couldn't sign ("+err.Error()+"); recorded unsigned")
		_, err = chain.Append(root, e, nil)
	}
	if err != nil {
		if cfg.SigningMode() == config.SigningRequired {
			return err
		}
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: "+err.Error())
	}
	return nil
}

// lookupSHA is the SHA-256 of root's lookup.yml ("" without one): approve
// entries record it (swim verify's authorized level).
func lookupSHA(root string) string {
	sum, _ := chain.HashFile(filepath.Join(root, lookup.FileName))
	return sum
}

// roundSection is the text a round's chain entry hashes: its lines from
// "== ROUND" to the end of its END block, trailing blank lines dropped (the
// next round's header adds one).
func roundSection(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// roundApprovals are the hashes of the approve entries recorded for lane n
// in run.
func roundApprovals(root, run string, n int) []string {
	links, _ := chain.Read(root)
	var out []string
	for _, l := range links {
		if l.Type == "approve" && l.Run == run && l.Lane == n {
			out = append(out, l.Hash)
		}
	}
	return out
}
