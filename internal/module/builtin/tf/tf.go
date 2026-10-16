// Package tf is the built-in tf module: Terraform (or OpenTofu) planned
// as the round's evidence, then applied from exactly that saved plan.
//
//	stage check
//	mod --evidence tf plan infra/orders
//	stage change
//	if guard ORDERS_ALLOW_APPLY "apply the orders plan"; then mod tf apply infra/orders; fi
//
// plan runs init (the lockfile read-only) and plan quietly, saves the plan
// file in the round's $SWIM_OUT/.swim-tf/, and prints only `terraform show`
// of it: stable text, so an evidence step's hash, and so an approval,
// covers the plan itself. apply applies that file and nothing else: it
// refuses one that's missing, or changed since plan saved it (its hash,
// and the hash of its shown text), and Terraform refuses a plan the state
// moved past. Plan and apply must be in one round.
package tf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod --evidence tf plan DIR [--var-file F]... [--var K=V]... [--bin B] [--no-init] [--lock-timeout D] | mod tf apply DIR [--bin B] [--lock-timeout D]"

// saved is what plan records beside its plan file, for apply.
type saved struct {
	Dir     string `yaml:"dir"`
	Bin     string `yaml:"bin"`
	PlanSHA string `yaml:"plan_sha256"`
	ShowSHA string `yaml:"show_sha256"`
	Changes bool   `yaml:"changes"`
}

// Run is mod tf plan|apply DIR ...
func Run(c *gomod.Ctx) error {
	a, err := parse(c.Args)
	if err != nil {
		return err
	}
	if len(a.pos) != 2 {
		return gomod.Usage{Msg: usage}
	}
	dir := a.pos[1]
	if filepath.IsAbs(dir) || strings.HasPrefix(filepath.ToSlash(filepath.Clean(dir)), "../") {
		return gomod.Usage{Msg: "tf: DIR is a directory inside the repo, relative to its root"}
	}
	out := c.Getenv("SWIM_OUT")
	if out == "" {
		return errors.New("tf: SWIM_OUT is unset (tf runs inside a lane's round)")
	}
	slug := chars.Squash(filepath.ToSlash(filepath.Clean(dir)), chars.Alnum, "_")
	if slug == "" {
		slug = "root"
	}
	base := filepath.Join(out, ".swim-tf", slug)
	switch a.pos[0] {
	case "plan":
		return plan(c, a, dir, base, slug)
	case "apply":
		if len(a.varFiles)+len(a.vars) > 0 || a.noInit {
			return gomod.Usage{Msg: "tf apply takes no variables: it applies the plan saved with them"}
		}
		return apply(c, a, dir, base)
	}
	return gomod.Usage{Msg: usage}
}

type args struct {
	pos              []string
	bin, lockTimeout string
	varFiles, vars   []string
	noInit           bool
}

// parse reads tf's arguments: --var-file and --var may repeat.
func parse(in []string) (args, error) {
	a := args{bin: "terraform"}
	for i := 0; i < len(in); i++ {
		name, val, has := strings.Cut(in[i], "=")
		take := func() (string, error) {
			if has {
				return val, nil
			}
			if i+1 >= len(in) {
				return "", gomod.Usage{Msg: name + " needs a value"}
			}
			i++
			return in[i], nil
		}
		var err error
		switch name {
		case "--bin":
			a.bin, err = take()
		case "--lock-timeout":
			a.lockTimeout, err = take()
		case "--var-file":
			var v string
			v, err = take()
			a.varFiles = append(a.varFiles, v)
		case "--var":
			var v string
			v, err = take()
			if err == nil && !strings.Contains(v, "=") {
				err = gomod.Usage{Msg: "--var takes NAME=VALUE"}
			}
			a.vars = append(a.vars, v)
		case "--no-init":
			a.noInit = !has
		default:
			if strings.HasPrefix(in[i], "-") {
				return a, gomod.Usage{Msg: "tf: unknown option " + in[i]}
			}
			a.pos = append(a.pos, in[i])
		}
		if err != nil {
			return a, err
		}
	}
	return a, nil
}

func plan(c *gomod.Ctx, a args, dir, base, slug string) error {
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return err
	}
	planFile := base + ".tfplan"
	os.Remove(planFile)
	// Quiet: an evidence step's output is the plan alone. Each command's
	// own output is shown only when it fails.
	if !a.noInit {
		if _, err := quiet(c, a.bin, "-chdir="+dir, "init", "-input=false", "-no-color", "-lockfile=readonly"); err != nil {
			return err
		}
	}
	args := []string{"-chdir=" + dir, "plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=" + planFile}
	if a.lockTimeout != "" {
		args = append(args, "-lock-timeout="+a.lockTimeout)
	}
	for _, f := range a.varFiles {
		args = append(args, "-var-file="+f)
	}
	for _, v := range a.vars {
		args = append(args, "-var="+v)
	}
	code, err := quiet(c, a.bin, args...)
	if err != nil && code != 2 {
		return err
	}
	changes := code == 2
	show, err := c.Capture(a.bin, "-chdir="+dir, "show", "-no-color", planFile)
	if err != nil {
		return err
	}
	planData, err := os.ReadFile(planFile)
	if err != nil {
		return fmt.Errorf("tf: the plan file: %w", err)
	}
	rec := saved{Dir: dir, Bin: a.bin, PlanSHA: hash(planData), ShowSHA: hash(show), Changes: changes}
	data, _ := yaml.Marshal(rec)
	if err := os.WriteFile(base+".yml", data, 0o600); err != nil {
		return err
	}
	os.Chmod(planFile, 0o600)
	c.Stdout.Write(show)
	if c.Output != nil {
		c.Output("tf."+slug+".changes", fmt.Sprint(changes))
	}
	return nil
}

func apply(c *gomod.Ctx, a args, dir, base string) error {
	if c.Getenv("SWIM_CHECK_ONLY") == "1" {
		fmt.Fprintf(c.Stdout, "tf: not applied: swim plan --check changes nothing\n")
		return nil
	}
	data, err := os.ReadFile(base + ".yml")
	if err != nil {
		return fmt.Errorf("tf: no plan of %s in this round: run `mod --evidence tf plan %s` before it (in the check stage)", dir, dir)
	}
	var rec saved
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("tf: the saved plan's record: %w", err)
	}
	if a.bin == "terraform" && rec.Bin != "" {
		a.bin = rec.Bin
	}
	planFile := base + ".tfplan"
	planData, err := os.ReadFile(planFile)
	if err != nil || hash(planData) != rec.PlanSHA {
		return fmt.Errorf("tf: the plan file of %s changed since plan saved it: not applied", dir)
	}
	show, err := c.Capture(a.bin, "-chdir="+dir, "show", "-no-color", planFile)
	if err != nil {
		return err
	}
	if hash(show) != rec.ShowSHA {
		return fmt.Errorf("tf: the saved plan of %s no longer shows what it showed at plan time: not applied", dir)
	}
	if !rec.Changes {
		fmt.Fprintf(c.Stdout, "tf: the plan of %s has no changes: nothing to apply\n", dir)
		return nil
	}
	fmt.Fprintf(c.Stdout, "tf: applying the plan of %s saved this round (%s)\n", dir, rec.PlanSHA[:12])
	args := []string{"-chdir=" + dir, "apply", "-input=false", "-no-color"}
	if a.lockTimeout != "" {
		args = append(args, "-lock-timeout="+a.lockTimeout)
	}
	cmd := c.Command(a.bin, append(args, planFile)...)
	cmd.Stdout, cmd.Stderr = c.Stdout, c.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tf: %s apply failed (%v)", a.bin, err)
	}
	return nil
}

// quiet runs a command and shows its output only if it fails; it returns
// the exit code too (plan's -detailed-exitcode says 2 for changes).
func quiet(c *gomod.Ctx, name string, args ...string) (int, error) {
	cmd := c.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	code := 1
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	if code == 2 && len(args) > 1 && args[1] == "plan" {
		return 2, err
	}
	c.Stderr.Write(buf.Bytes())
	return code, fmt.Errorf("tf: %s %s failed (exit %d)", name, args[1], code)
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
