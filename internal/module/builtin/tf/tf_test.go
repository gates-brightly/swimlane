package tf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/module/gomod/gomodtest"
)

// fakeTF logs its calls; plan exits $PLAN_CODE (default 2: changes) and
// writes the plan file; show prints the plan file's contents.
const fakeTF = `echo "$*" >>"$FAKE_DIR/calls"
case "$2" in
  init) echo "Initializing the backend... $(date +%s%N)" ;;
  plan) for a in "$@"; do case "$a" in -out=*) echo "plan-v1 $PLAN_TEXT" >"${a#-out=}" ;; esac; done
        echo "Refreshing state... $(date +%s%N)"; exit "${PLAN_CODE:-2}" ;;
  show) cat "$4" | sed 's/^/  # /'; echo "Plan: 1 to add, 0 to change, 0 to destroy." ;;
  apply) echo "Apply complete! Resources: 1 added. (plan ${@: -1})" ;;
  *) exit 9 ;;
esac`

func newFake(t *testing.T, out string, env map[string]string, args ...string) *gomodtest.Fake {
	base := map[string]string{"SWIM_OUT": out}
	for k, v := range env {
		base[k] = v
	}
	f := gomodtest.NewFake(t.TempDir(), base, false, args...)
	f.Tool("terraform", fakeTF)
	return f
}

func TestPlanThenApply(t *testing.T) {
	out := t.TempDir()
	p := newFake(t, out, nil, "plan", "infra/orders", "--var-file", "prod.tfvars", "--var", "region=us-east-1")
	if err := Run(&p.Ctx); err != nil {
		t.Fatalf("%v\n%s%s", err, p.StdoutB.String(), p.StderrB.String())
	}
	// Only the shown plan: stable text for the evidence hash.
	if got := p.StdoutB.String(); got != "  # plan-v1 \nPlan: 1 to add, 0 to change, 0 to destroy.\n" {
		t.Errorf("plan output: %q", got)
	}
	calls, _ := os.ReadFile(filepath.Join(p.Dir, "calls"))
	for _, want := range []string{"-chdir=infra/orders init -input=false -no-color -lockfile=readonly", "-detailed-exitcode", "-var-file=prod.tfvars", "-var=region=us-east-1"} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("calls lack %q:\n%s", want, calls)
		}
	}
	if p.Outputs["tf.infra_orders.changes"] != "true" {
		t.Errorf("outputs: %v", p.Outputs)
	}

	a := newFake(t, out, nil, "apply", "infra/orders")
	if err := Run(&a.Ctx); err != nil {
		t.Fatalf("%v\n%s%s", err, a.StdoutB.String(), a.StderrB.String())
	}
	if !strings.Contains(a.StdoutB.String(), "Apply complete!") || !strings.Contains(a.StdoutB.String(), ".swim-tf/infra_orders.tfplan") {
		t.Errorf("apply: %s", a.StdoutB.String())
	}

	// A plan file changed after plan saved it is refused.
	os.WriteFile(filepath.Join(out, ".swim-tf", "infra_orders.tfplan"), []byte("plan-v2 tampered\n"), 0o600)
	a = newFake(t, out, nil, "apply", "infra/orders")
	if err := Run(&a.Ctx); err == nil || !strings.Contains(err.Error(), "changed since plan saved it") {
		t.Errorf("tampered: %v", err)
	}
}

func TestPlanCases(t *testing.T) {
	out := t.TempDir()
	// No plan this round: apply refuses.
	a := newFake(t, out, nil, "apply", "infra/x")
	if err := Run(&a.Ctx); err == nil || !strings.Contains(err.Error(), "no plan of infra/x in this round") {
		t.Errorf("no plan: %v", err)
	}
	// No changes: apply has nothing to do.
	p := newFake(t, out, map[string]string{"PLAN_CODE": "0"}, "plan", "infra/x", "--no-init")
	if err := Run(&p.Ctx); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(p.Dir, "calls"))
	if strings.Contains(string(calls), " init ") {
		t.Errorf("--no-init ran init: %s", calls)
	}
	a = newFake(t, out, nil, "apply", "infra/x")
	if err := Run(&a.Ctx); err != nil || !strings.Contains(a.StdoutB.String(), "nothing to apply") {
		t.Errorf("no changes: %v %s", err, a.StdoutB.String())
	}
	// A failed plan shows terraform's own output.
	p = newFake(t, out, map[string]string{"PLAN_CODE": "1"}, "plan", "infra/x")
	if err := Run(&p.Ctx); err == nil || !strings.Contains(p.StderrB.String(), "Refreshing state") {
		t.Errorf("failed plan: %v %s", err, p.StderrB.String())
	}
	// A read-only check never applies.
	a = newFake(t, out, map[string]string{"SWIM_CHECK_ONLY": "1"}, "apply", "infra/x")
	if err := Run(&a.Ctx); err != nil || !strings.Contains(a.StdoutB.String(), "not applied") {
		t.Errorf("check: %v", err)
	}
	for _, bad := range [][]string{{"apply", "x", "--var", "a=b"}, {"plan", "/abs"}, {"plan"}, {"destroy", "x"}, {"plan", "x", "--var", "novalue"}} {
		f := newFake(t, out, nil, bad...)
		if err := Run(&f.Ctx); err == nil {
			t.Errorf("%v passed", bad)
		}
	}
}
