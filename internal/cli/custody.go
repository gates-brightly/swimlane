package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/status"
)

// runnerKey is the key this swim signs chain entries with: nil (and no
// error) when signing is off, or auto and no key loads.
func runnerKey(cfg *config.Config) (*sign.Key, error) { return authority.Key(cfg) }

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
		// A temporary name of its own: lanes start at once.
		if err := fsutil.WriteAtomic(dst, data, 0o644); err != nil {
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
		if l := st.Get(n); l != nil && l.ScriptSHA != "" {
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
		if l := st.Get(n); l != nil {
			return l.ManifestSHA
		}
	}
	return ""
}

// laneProbes are the Observe: probes of lane n's round (this
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
		return lane.ProbesIn(data, side)
	}
	info, _ := lane.ReadScript(root, n)
	return info.Observe
}

// record appends an entry to the chain, signed by the runner's key when
// there is one (authority.Record). Under swim run a round's entry goes to
// the launcher instead, which signs it with the run key: a lane needs no
// key of its own.
func record(root string, cfg *config.Config, e chain.Entry) error {
	if tok := os.Getenv(approval.TokenEnv); launched() && tok != "" && e.Type == "round" {
		_, err := approval.Record(os.Getenv(approval.EnvVar), tok, e)
		return err
	}
	_, err := authority.Record(root, cfg, e)
	return err
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
// in run, and the guards among them approved by -y (method auto), once each.
func roundApprovals(root, run string, n int) (hashes, auto []string) {
	links, _ := chain.Read(root)
	for _, l := range links {
		if l.Type == "approve" && l.Run == run && l.Lane == n {
			hashes = append(hashes, l.Hash)
			if l.Method == "auto" && !slices.Contains(auto, l.Guard) {
				auto = append(auto, l.Guard)
			}
		}
	}
	return hashes, auto
}
