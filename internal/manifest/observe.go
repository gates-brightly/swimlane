package manifest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/lane"
)

// ProbeTimeout caps one Observe: probe.
var ProbeTimeout = 30 * time.Second

// Observe runs a round's probes in root with env (nil: swim's own) and
// returns each fact: a command's trimmed output (or "error: ..."), or for
// a path probe the binary PATH finds and its hash ("(not found)").
func Observe(root string, probes []lane.Probe, env []string) map[string]string {
	if len(probes) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, p := range probes {
		for _, c := range p.Path {
			path, err := lookPath(root, c, env)
			if err != nil {
				out["path."+c] = "(not found)"
				continue
			}
			sum := fileHash(path)
			if len(sum) > 12 {
				sum = sum[:12]
			}
			out["path."+c] = path + "@sha256:" + sum
		}
		if p.Cmd == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
		cmd := exec.CommandContext(ctx, "bash", "-c", p.Cmd)
		cmd.Dir, cmd.Env = root, env
		b, err := cmd.Output()
		cancel()
		v := strings.TrimSpace(string(b))
		if len(v) > 200 {
			v = v[:200] + "..."
		}
		if err != nil {
			v = "error: " + err.Error()
		}
		out[p.Name] = v
	}
	return out
}

// lookPath finds a command on env's PATH (swim's own when env sets none),
// past root/.swim/bin (where older swims put a git shim): the launcher and
// the lane must see the same binary.
func lookPath(root, name string, env []string) (string, error) {
	path := os.Getenv("PATH")
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], "PATH="); ok {
			path = v
			break
		}
	}
	shim := filepath.Join(root, ".swim", "bin")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || filepath.Clean(dir) == shim {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// ObservedDiff lists the facts that changed between approval (was) and
// now: "aws_account was 123456789012 at approval, now 210987654321".
func ObservedDiff(was, now map[string]string) []string {
	var keys []string
	for k := range was {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		if now[k] != was[k] {
			out = append(out, fmt.Sprintf("%s was %s at approval, now %s", k, or(was[k], "(none)"), or(now[k], "(none)")))
		}
	}
	return out
}

// ObservedText is facts as one line: "aws_account=1234, path.aws=/usr/...".
func ObservedText(facts map[string]string) string {
	var keys []string
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + facts[k]
	}
	return strings.Join(parts, ", ")
}
