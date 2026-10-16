package e2e

import (
	"strings"
	"testing"
)

// A step expected to fail (run --expect-fail) passes on a non-zero exit,
// and fails on exit 0 or a code it wasn't expected to exit with.
func TestExpectFail(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "negative tests", `run --expect-fail "bad token refused" bash -c 'exit 3'
run --expect-fail=1,3 "listed code" bash -c 'exit 3'
run --expect-fail "accepted anyway" true
run --expect-fail=2 "wrong code" bash -c 'exit 3'
run --expect-fail --timeout 1s "too slow" sleep 5
run --expect-fail "no such command" swim-no-such-command
run "after" true`)
	r.swim("run", "1", "--plain")
	log := r.log(1)
	contains(t, "log", log,
		"PASS  bad token refused (failed as expected: exit 3)",
		"PASS  listed code (failed as expected: exit 3)",
		"FAIL  accepted anyway (exit 0, but it was expected to fail)",
		"FAIL  wrong code (exit 3, expected to fail with 2)",
		"FAIL  too slow (timeout",
		"FAIL  no such command (exit 127: the command didn't start",
		"PASS  after")
	if l := r.status().Get(1); l.Pass != 3 || l.Fail != 4 {
		t.Errorf("status passed %d failed %d, want 3 and 4\n%s", l.Pass, l.Fail, log)
	}

	// swim step by hand: the exit code follows the verdict.
	if _, code := r.swim("step", "--expect-fail", "--", "false"); code != 0 {
		t.Errorf("step --expect-fail false: exit %d, want 0", code)
	}
	if _, code := r.swim("step", "--expect-fail", "--", "true"); code != 1 {
		t.Errorf("step --expect-fail true: exit %d, want 1", code)
	}
	out, code := r.swim("step", "--expect-fail", "--retry", "2", "--", "false")
	if code != 2 || !strings.Contains(out, "don't go together") {
		t.Errorf("--expect-fail with --retry: exit %d\n%s", code, out)
	}
	out, code = r.swim("step", "--expect-fail=0", "--", "false")
	if code != 2 || !strings.Contains(out, "non-zero exit codes") {
		t.Errorf("--expect-fail=0: exit %d\n%s", code, out)
	}
}
