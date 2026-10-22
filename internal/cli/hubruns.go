package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/hub"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
	"github.com/gates-brightly/swimlane/internal/runrec"
)

// Finished runs go to the hub as well as snapshots (hub.RunReport): each
// run record under .swim/runs/ with the round every lane ran in it, from
// the lanes' logs (masked of secrets already), their step output only
// where swim.yml says hub.logs: stream. swim watch offers the
// newest hubRunsRecent each report; .swim/hub-runs.yml remembers what the
// hub took, and under which worker token, so a new enrollment (a hub that
// lost its state) gets them again.

const hubRunsRecent = 20

// hubRunsFile is .swim/hub-runs.yml.
type hubRunsFile struct {
	Token string            `yaml:"token_sha256"`
	Sent  map[string]string `yaml:"sent"` // run id -> its record's mtime when sent
}

func hubRunsPath(root string) string { return filepath.Join(root, ".swim", "hub-runs.yml") }

// sendHubRuns sends the hub the recent runs it hasn't taken (or whose
// record changed since). It stops at the first failure, to try again next
// report; a refusal that won't change is marked sent, so it isn't retried.
func sendHubRuns(root string, cfg *config.Config, h *hubFile, c *hub.Client) error {
	sum := sha256.Sum256([]byte(h.URL + "\x00" + c.ID + "\x00" + c.WorkerToken))
	token := hex.EncodeToString(sum[:])
	var f hubRunsFile
	if data, err := os.ReadFile(hubRunsPath(root)); err == nil {
		yaml.Unmarshal(data, &f)
	}
	if f.Token != token || f.Sent == nil {
		f = hubRunsFile{Token: token, Sent: map[string]string{}}
	}
	recent := recentRuns(root, hubRunsRecent)
	changed := false
	var failed error
	for _, id := range recent {
		fi, err := os.Stat(runrec.Path(root, id))
		if err != nil {
			continue
		}
		mod := fi.ModTime().UTC().Format(time.RFC3339Nano)
		if f.Sent[id] == mod {
			continue
		}
		r, ok, err := runrec.Load(root, id)
		if err != nil || !ok {
			continue
		}
		permanent, err := c.PutRun(hubRunReport(root, r, cfg.HubLogs() == config.HubLogsStream))
		if err != nil && !permanent {
			failed = fmt.Errorf("run %s: %w", id, err)
			break
		}
		f.Sent[id], changed = mod, true
	}
	// Remember only what's still offered.
	keep := map[string]bool{}
	for _, id := range recent {
		keep[id] = true
	}
	for id := range f.Sent {
		if !keep[id] {
			delete(f.Sent, id)
			changed = true
		}
	}
	if changed {
		data, _ := yaml.Marshal(&f)
		tmp := fmt.Sprintf("%s.%d.tmp", hubRunsPath(root), os.Getpid())
		if err := os.WriteFile(tmp, data, 0o600); err == nil {
			os.Rename(tmp, hubRunsPath(root))
		}
	}
	return failed
}

// recentRuns are the newest n run ids with a record (a run id starts
// with its time, to the second, so by id is by start).
func recentRuns(root string, n int) []string {
	files, _ := filepath.Glob(filepath.Join(runrec.Dir(root), "*.yml"))
	var ids []string
	for _, f := range files {
		ids = append(ids, strings.TrimSuffix(filepath.Base(f), ".yml"))
	}
	sort.Strings(ids)
	if len(ids) > n {
		ids = ids[len(ids)-n:]
	}
	return ids
}

// hubRunReport is run r with each lane's round in it, from the lane's
// log: with step output only when output is set (hub.logs: stream), and
// without it when the report would be over hub.RunMax.
func hubRunReport(root string, r *runrec.Run, output bool) *hub.RunReport {
	rep := &hub.RunReport{Run: *r, Jobs: []logparse.Doc{}}
	for _, l := range r.Lanes {
		rounds, err := logparse.RoundsInFile(lane.Log(root, l.Lane))
		if err != nil {
			continue
		}
		for i := len(rounds) - 1; i >= 0; i-- {
			if rounds[i].Run == r.Run {
				rep.Jobs = append(rep.Jobs, rounds[i].Doc(l.Lane))
				break
			}
		}
	}
	if !output {
		for i := range rep.Jobs {
			for j := range rep.Jobs[i].Results {
				rep.Jobs[i].Results[j].Output = nil
			}
		}
	} else if size := reportSize(rep); size > hub.RunMax {
		for i := range rep.Jobs {
			for j := range rep.Jobs[i].Results {
				if res := &rep.Jobs[i].Results[j]; len(res.Output) > 0 {
					res.Output = []string{fmt.Sprintf("(output left out: the run's report was %d MiB, over the hub's %d MiB; swim log %d has it)",
						size>>20, hub.RunMax>>20, rep.Jobs[i].Lane)}
				}
			}
		}
	}
	return rep
}

func reportSize(rep *hub.RunReport) int {
	data, _ := json.Marshal(rep)
	return len(data)
}
