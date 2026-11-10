package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/finding"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/queue"
)

// retired are files and directories directly under .swim/ that an older
// swim wrote and this one never reads, with what wrote them.
var retired = map[string]string{
	"hub.yml":              "swim hub",
	"hub-runs.yml":         "swim hub",
	"headless.lock":        "headless runs",
	"headless.pid":         "headless runs",
	"headless.log":         "headless runs",
	"groups":               "lane group files",
	"observations":         "swim observe",
	"knowledge":            "swim observe",
	"out":                  "$SWIM_OUT",
	"checkpoints":          "run --checkpoint",
	"checkpoints.jsonl":    "chain anchors",
	"checkpoints.sent.yml": "chain anchors",
	"cleaned":              "swim clean",
	"modules":              "modules",
	"evidence":             "evidence-bound approvals",
	"signatures":           "signed approvals",
	"signoffs":             "# Signoff:",
	"migrations":           "script migrations",
	"msg":                  "swim msg",
	"pause":                "swim pause",
	"earmarks":             "swim report --earmark",
}

// retiredLaneRE: per-lane files of group lanes (lane1.2.claim) and of swim
// pause (lane3.pause).
var retiredLaneRE = regexp.MustCompile(`^lane[0-9]+\.[0-9]+\.(claim|pid|stop|end)$|^lane[0-9]+\.pause$`)

// retiredWhat says what wrote a retired name under .swim/, or "".
func retiredWhat(name string) string {
	if w, ok := retired[name]; ok {
		return w
	}
	if retiredLaneRE.MatchString(name) {
		if strings.HasSuffix(name, ".pause") {
			return "swim pause"
		}
		return "lane group files"
	}
	return ""
}

func checkLeftover(o *Options) []finding.Finding {
	entries, err := os.ReadDir(filepath.Join(o.Root, ".swim"))
	if err != nil {
		return nil
	}
	var out []finding.Finding
	for _, e := range entries {
		what := retiredWhat(e.Name())
		if what == "" {
			continue
		}
		file := filepath.Join(".swim", e.Name())
		if e.IsDir() {
			file += "/"
		}
		out = append(out, fd(finding.Warn, "leftover", file, "swim doctor --fix (deletes it)",
			"swim no longer reads it (left by %s)", what))
	}
	return append(out, checkOldLock(o)...)
}

// oldLock is the repo-root file swim before v0.20261105.5 wrote (swim init,
// swim lock) and asked you to commit. Only an older swim writes it now:
// the finding names the version that wrote it, and says that one coming
// back means such a swim still runs here (the file may just be old).
const oldLock = ".swim.lock"

// checkOldLock finds .swim.lock. --fix deletes an untracked one; a tracked
// one is the operator's to git rm (swim never writes to git), as is one git
// can't answer for.
func checkOldLock(o *Options) []finding.Finding {
	p := filepath.Join(o.Root, oldLock)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	by := "a swim older than v0.20261105.5"
	var v struct {
		Version string `yaml:"version"`
	}
	if yaml.Unmarshal(data, &v) == nil && v.Version != "" {
		by = "swim " + v.Version
	}
	fix := "swim doctor --fix (deletes it)"
	if tracked, err := lane.Tracked(o.Root, p); err != nil {
		fix = "delete it or git rm it yourself; --fix won't: " + err.Error()
	} else if tracked {
		fix = "git rm " + oldLock + " (swim never writes to git)"
	}
	return []finding.Finding{fd(finding.Warn, "leftover", oldLock, fix,
		"swim no longer reads it; it was written by %s. If it comes back, an old swim is still running here", by)}
}

func checkExpiredClaims(o *Options) []finding.Finding {
	files, _ := filepath.Glob(filepath.Join(lane.ClaimDir(o.Root), "lane.*.yml"))
	sort.Strings(files)
	var out []finding.Finding
	for _, p := range files {
		c, ok := expiredClaim(p)
		if !ok {
			continue
		}
		out = append(out, fd(finding.Warn, "claim-expired", o.rel(p), "swim doctor --fix (deletes it)",
			"a claim by %s (session %s) that expired %s", c.Owner, c.Session, c.ExpiresAt().Local().Format("2006-01-02 15:04")))
	}
	return out
}

// expiredClaim reads a claim file and reports whether its lease ran out.
func expiredClaim(p string) (*lane.Claim, bool) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var c lane.Claim
	if yaml.Unmarshal(data, &c) != nil || c.Session == "" {
		return nil, false
	}
	return &c, !time.Now().Before(c.ExpiresAt())
}

func checkWatchGone(o *Options) []finding.Finding {
	files, _ := filepath.Glob(filepath.Join(queue.WorkersDir(o.Root), "*.yml"))
	sort.Strings(files)
	var out []finding.Finding
	for _, p := range files {
		w, why := goneWatch(p)
		if why == "" {
			continue
		}
		out = append(out, fd(finding.Warn, "watch-gone", o.rel(p), "swim doctor --fix (deletes it)",
			"swim watch on %s (pid %d) %s, but its presence file is still here", w.Host, w.PID, why))
	}
	return out
}

// goneWatch reads a swim watch's presence file and says why that watch is
// gone ("" while it's up): its process is (this host), or its heartbeat
// is older than its lease (another host).
func goneWatch(p string) (*queue.Presence, string) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, ""
	}
	var w queue.Presence
	if yaml.Unmarshal(data, &w) != nil || w.PID == 0 {
		return nil, ""
	}
	if proc.Local(w.Host) {
		if proc.Same(w.PID, w.Start) {
			return nil, ""
		}
		return &w, "has exited"
	}
	beat, err := time.Parse(time.RFC3339, w.Heartbeat)
	if err != nil || time.Since(beat) <= w.LeaseFor() {
		return nil, ""
	}
	return &w, "last beat " + time.Since(beat).Round(time.Minute).String() + " ago"
}

// removeLeftover deletes a claim-expired, watch-gone, leftover or stub finding's
// file, after checking again that it still is one.
func (o *Options) removeLeftover(f finding.Finding) (bool, error) {
	rel := strings.TrimSuffix(f.File, "/")
	p := filepath.Join(o.Root, rel)
	if filepath.IsAbs(rel) {
		p = rel
	}
	dir := filepath.Dir(p)
	var ok bool
	switch f.Code {
	case "claim-expired":
		_, ok = expiredClaim(p)
		ok = ok && dir == lane.ClaimDir(o.Root)
	case "watch-gone":
		_, why := goneWatch(p)
		ok = why != "" && dir == queue.WorkersDir(o.Root)
	case "leftover":
		ok = dir == filepath.Join(o.Root, ".swim") && retiredWhat(filepath.Base(p)) != ""
		if p == filepath.Join(o.Root, oldLock) {
			tracked, err := lane.Tracked(o.Root, p)
			ok = err == nil && !tracked
		}
	case "stub":
		// Only one git says it doesn't track: when git can't answer, the
		// stub stays (the finding says why).
		for _, s := range stubFiles(o.Root) {
			if s == p {
				tracked, err := lane.Tracked(o.Root, p)
				ok = err == nil && !tracked
			}
		}
	}
	if !ok {
		return false, nil
	}
	if err := os.RemoveAll(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// checkStubs finds the "nothing ready" stubs an older swim archive left
// in place of a lane's script: the lane is empty, so the file is clutter.
// --fix deletes one git doesn't track; one it does is the operator's to
// git rm (swim never writes to git). When git can't say, --fix leaves it.
func checkStubs(o *Options) []finding.Finding {
	var out []finding.Finding
	for _, p := range stubFiles(o.Root) {
		fix := "swim doctor --fix (deletes it)"
		if tracked, err := lane.Tracked(o.Root, p); err != nil {
			fix = "delete it or git rm it yourself; --fix won't: " + err.Error()
		} else if tracked {
			fix = "git rm " + o.rel(p) + " (swim never writes to git)"
		}
		out = append(out, fd(finding.Warn, "stub", o.rel(p), fix,
			"an archived lane's stub, left by an older swim archive"))
	}
	return out
}

// stubFiles lists the lane scripts in root that are stubs, of lanes not
// running.
func stubFiles(root string) []string {
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		n, _, ok := lane.ParseScriptName(e.Name())
		if !ok || e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		if info, _ := lane.ReadScript(root, n); !info.Stub || lane.Script(root, n) != p {
			continue
		}
		if _, running := lane.Running(root, n); running {
			continue
		}
		out = append(out, p)
	}
	return out
}
