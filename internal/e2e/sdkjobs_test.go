package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/status"
)

// The jobs every language's lane declares, in its own comments.
const sdkJobsHeader = `Round: jobs
swim job 1.1
Round: first
Always: yes

swim job 1.2
Round: group

swim job 1.2.1
Round: a

swim job 1.2.2
Round: b
`

func commented(prefix string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(sdkJobsHeader, "\n"), "\n") {
		if l == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix + " " + l + "\n")
	}
	return b.String()
}

// A lane in an SDK language marks its jobs in comments; each job runs the
// program, and jobs() calls its function ("1.2" covers 1.2.1 and 1.2.2).
func TestSDKJobs(t *testing.T) {
	for _, lang := range []string{"py", "ts", "go"} {
		t.Run(lang, func(t *testing.T) {
			r := newRepo(t, "")
			r.env = append(r.env, "PYTHONPATH="+filepath.Join(repoRoot(), "sdk", "python"))
			switch lang {
			case "py":
				if _, err := exec.LookPath("python3"); err != nil {
					t.Skip("python3 not installed")
				}
				r.write("lane.1.py", "#!/usr/bin/env python3\n"+commented("#")+`import swim
swim.jobs({
    "1.1": lambda r: r.run("which", ["echo", "job", r.job]),
    "2": lambda r: r.run("which", ["echo", "job", r.job]),
})
`, 0o755)
			case "ts":
				if out, err := exec.Command("node", "--experimental-strip-types", "-e", "1").CombinedOutput(); err != nil {
					t.Skipf("node can't run TypeScript: %v %s", err, out)
				}
				r.write("lane.1.ts", commented("//")+`import { jobs } from "`+filepath.Join(repoRoot(), "sdk", "ts", "swim.ts")+`";
const which = async (r) => { await r.run("which", ["echo", "job", r.job]); };
await jobs({ "1.1": which, "1.2": which });
`, 0o644)
				r.write("swim.yml", "runners:\n  ts: node --experimental-strip-types --no-warnings\n", 0o644)
			case "go":
				// The SDK as a local package, so go run needs nothing fetched.
				os.MkdirAll(filepath.Join(r.root, "swim"), 0o755)
				for _, f := range []string{"swim.go", "secret.go"} {
					r.write(filepath.Join("swim", f), string(mustRead(t, filepath.Join(repoRoot(), "sdk", "go", "swim", f))), 0o644)
				}
				r.write("go.mod", "module lanes\n\ngo 1.22\n", 0o644)
				r.write("swim.yml", "runners:\n  go: go run\n", 0o644)
				r.write("lane.1.go", commented("//")+`
package main

import (
	"os"

	"lanes/swim"
)

func which(r *swim.R) error {
	_, err := r.Run("which", "echo", "job", r.Job())
	return err
}

func main() {
	os.Exit(swim.Jobs(map[string]func(*swim.R) error{"1.1": which, "1.2": which}))
}
`, 0o644)
			}
			jobLog := func(k string) string {
				data, _ := os.ReadFile(filepath.Join(r.root, ".swim", "logs", "agent1."+k+".log"))
				return string(data)
			}

			contains(t, "plan", r.mustSwim("plan"), "jobs, in order: 1.1 first [*] → 1.2.1 a → 1.2.2 b")
			out := r.mustSwim("run", "1", "--plain")
			if st := r.status().Get(1).State; st != status.Passed {
				t.Fatalf("lane 1 = %s\n%s", st, out)
			}
			contains(t, "job 1.1", jobLog("1"), "job 1.1")
			contains(t, "job 1.2.1", jobLog("2.1"), "job 1.2.1")
			contains(t, "job 1.2.2", jobLog("2.2"), "job 1.2.2")

			r.mustSwim("run", "1.2.2", "--plain")
			if n := strings.Count(jobLog("2.2"), "== ROUND "); n != 2 || strings.Count(jobLog("2.1"), "== ROUND ") != 1 {
				t.Errorf("swim run 1.2.2: 1.2.2 ran %d times, want 2 (and 1.2.1 once)", n)
			}

			// A job with no function ends its round as failed, saying so.
			if lang == "py" {
				src := strings.Replace(string(mustRead(t, filepath.Join(r.root, "lane.1.py"))), `"1.1": lambda`, `"9": lambda`, 1)
				r.write("lane.1.py", src, 0o755)
				if _, code := r.swim("run", "1.1", "--plain"); code == 0 {
					t.Error("job 1.1 with no function passed")
				}
				contains(t, "job 1.1 log", jobLog("1"), "no function for job 1.1")
			}
		})
	}
}
