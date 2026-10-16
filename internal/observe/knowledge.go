package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// Knowledge is what keeps being true: an observation of the same kind, for
// the same lane and step, in at least KnowledgeMin of the last Window runs
// the lane ran in. It's computed from the runs' observations, never
// stored as a decision: no entry changes a timeout, a retry, a skip or an
// approval. A person accepts one into the repo's knowledge/steps.yml
// (committed, read by the next session and swim plan), or rejects it.

// KnowledgeSchema names knowledge/steps.yml.
const KnowledgeSchema = "swim.knowledge/v1"

// An Entry is one piece of knowledge.
type Entry struct {
	ID     string `yaml:"id"`
	Kind   string `yaml:"kind"`
	Lane   int    `yaml:"lane"`
	Step   string `yaml:"step,omitempty"`
	Claim  string `yaml:"claim"`
	Seen   int    `yaml:"seen"`   // runs it was observed in, of Of
	Of     int    `yaml:"of"`     // the lane's runs looked at
	First  string `yaml:"first"`  // the first run it was seen in
	Latest string `yaml:"latest"` // the latest
	// Set when accepted.
	AcceptedBy string `yaml:"accepted_by,omitempty"`
	AcceptedAt string `yaml:"accepted_at,omitempty"`
	Note       string `yaml:"note,omitempty"`
}

// KnowledgeFile is knowledge/steps.yml: the entries people accepted.
type KnowledgeFile struct {
	Schema  string  `yaml:"schema"`
	Entries []Entry `yaml:"entries"`
}

// KnowledgePath is the committed knowledge file.
func KnowledgePath(root string) string { return filepath.Join(root, "knowledge", "steps.yml") }

// rejectedPath lists the entries someone rejected (not committed: it's
// one operator's say).
func rejectedPath(root string) string {
	return filepath.Join(root, ".swim", "knowledge", "rejected.yml")
}

// knowledgeMin and retireAfter: an entry is proposed once seen in this
// many runs, and retired after this many of the lane's runs without it.
const (
	knowledgeMin = 3
	retireAfter  = 5
)

// entryID is an entry's stable id: its kind, lane and a hash of its step.
func entryID(kind string, lane int, step string) string {
	h := sha256.Sum256([]byte(step))
	return fmt.Sprintf("k-%s-%d-%s", kind, lane, hex.EncodeToString(h[:])[:8])
}

// A RunObs is one run's observations, and the lanes that ran in it.
type RunObs struct {
	Run   string
	Lanes map[int]bool
	Obs   []Observation
}

// Propose is the knowledge runs (oldest first) support: each kind, lane and
// step seen in at least knowledgeMin of the lane's last window runs.
// Waits (no step) aren't knowledge.
func Propose(runs []RunObs, window int) []Entry {
	if window <= 0 {
		window = 20
	}
	type key struct {
		kind string
		lane int
		step string
	}
	byLane := map[int][]RunObs{}
	for _, r := range runs {
		for n, ran := range r.Lanes {
			if ran {
				byLane[n] = append(byLane[n], r)
			}
		}
	}
	entries := map[key]*Entry{}
	for n, rs := range byLane {
		if len(rs) > window {
			rs = rs[len(rs)-window:]
		}
		for _, r := range rs {
			seen := map[key]bool{}
			for _, o := range r.Obs {
				k := key{o.Kind, o.Lane, o.Step}
				if o.Lane != n || o.Step == "" || seen[k] {
					continue
				}
				seen[k] = true
				e := entries[k]
				if e == nil {
					e = &Entry{ID: entryID(o.Kind, o.Lane, o.Step), Kind: o.Kind, Lane: o.Lane, Step: o.Step, First: r.Run}
					entries[k] = e
				}
				e.Seen++
				e.Latest = r.Run
				e.Claim = o.Text
			}
		}
		for k, e := range entries {
			if k.lane == n {
				e.Of = len(rs)
			}
		}
	}
	var out []Entry
	for _, e := range entries {
		if e.Seen >= knowledgeMin {
			e.Claim = claim(*e)
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// claim is an entry's sentence: what keeps being observed.
func claim(e Entry) string {
	what := map[string]string{
		Timeout: "hits its time limit", Retried: "needs retries to pass", Slow: "runs much slower than its usual time",
		Fast: "runs far faster than its usual time", Failing: "keeps failing", Dry: "runs as a dry run (nobody approves it)",
		Drift: "drifts", Flaky: "is flaky (its steps need retries or fail)",
	}[e.Kind]
	if what == "" {
		what = e.Kind
	}
	return fmt.Sprintf("swim %d %q %s: %d of its last %d runs", e.Lane, e.Step, what, e.Seen, e.Of)
}

// Retired says why an accepted entry no longer holds ("" if it does): the
// lane ran retireAfter times in a row without it.
func Retired(e Entry, runs []RunObs) string {
	clean := 0
	for i := len(runs) - 1; i >= 0 && clean < retireAfter; i-- {
		r := runs[i]
		if !r.Lanes[e.Lane] {
			continue
		}
		for _, o := range r.Obs {
			if o.Kind == e.Kind && o.Lane == e.Lane && o.Step == e.Step {
				return ""
			}
		}
		clean++
	}
	if clean >= retireAfter {
		return fmt.Sprintf("not observed in swim %d's last %d runs", e.Lane, retireAfter)
	}
	return ""
}

// History is the runs' observations, oldest first: each recorded run's
// observations file and the lanes its run record says ran.
func History(root string) ([]RunObs, error) {
	recs, err := runrec.List(root)
	if err != nil {
		return nil, err
	}
	var out []RunObs
	for _, rec := range recs {
		f, ok, err := Load(root, rec.Run)
		if err != nil || !ok {
			continue
		}
		r := RunObs{Run: rec.Run, Lanes: map[int]bool{}, Obs: f.Observations}
		for _, l := range rec.Lanes {
			if l.State != "skipped" {
				r.Lanes[l.Lane] = true
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// LoadKnowledge reads the committed knowledge (none: an empty file).
func LoadKnowledge(root string) (*KnowledgeFile, error) {
	k := &KnowledgeFile{Schema: KnowledgeSchema}
	data, err := os.ReadFile(KnowledgePath(root))
	if os.IsNotExist(err) {
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, k); err != nil {
		return nil, fmt.Errorf("%s: %w", KnowledgePath(root), err)
	}
	return k, nil
}

// Rejected are the entry ids someone rejected.
func Rejected(root string) map[string]bool {
	var ids []string
	if data, err := os.ReadFile(rejectedPath(root)); err == nil {
		yaml.Unmarshal(data, &ids)
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// Accept writes e into the committed knowledge, as accepted by who.
func Accept(root string, e Entry, who, note string) error {
	k, err := LoadKnowledge(root)
	if err != nil {
		return err
	}
	for _, have := range k.Entries {
		if have.ID == e.ID {
			return fmt.Errorf("%s is accepted already (%s)", e.ID, KnowledgePath(root))
		}
	}
	e.AcceptedBy, e.AcceptedAt, e.Note = who, time.Now().UTC().Format(time.RFC3339), note
	k.Entries = append(k.Entries, e)
	data, err := yaml.Marshal(k)
	if err != nil {
		return err
	}
	head := "# What keeps being true about this repo's lanes, accepted by people from\n" +
		"# swim observe's proposals (swim observe knowledge). Read by swim plan and\n" +
		"# the next session; it decides nothing. Commit it.\n"
	if err := os.MkdirAll(filepath.Dir(KnowledgePath(root)), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(KnowledgePath(root), []byte(head+string(data)), 0o644)
}

// Reject keeps id from being proposed again.
func Reject(root, id string) error {
	r := Rejected(root)
	r[id] = true
	var ids []string
	for k := range r {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	data, _ := yaml.Marshal(ids)
	if err := os.MkdirAll(filepath.Dir(rejectedPath(root)), 0o755); err != nil {
		return err
	}
	return fsutil.WriteAtomic(rejectedPath(root), data, 0o644)
}

// ForLane is the accepted knowledge about lane n, as lines for swim plan.
func ForLane(k *KnowledgeFile, n int) []string {
	var out []string
	for _, e := range k.Entries {
		if e.Lane == n {
			out = append(out, strings.TrimPrefix(e.Claim, fmt.Sprintf("swim %d ", n)))
		}
	}
	return out
}
