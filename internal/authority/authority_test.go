package authority

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gates-brightly/swimlane/internal/lookup"
)

// A guard whose lookup.yml rule says evidence: true can't be approved:
// Unapprovable says why, and Signed counts nothing for it. Other guards,
// and a lookup.yml that doesn't load (Signed says that), aren't affected.
func TestEvidenceRuleUnapprovable(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, lookup.FileName), []byte("require:\n  - guards: [ORDERS_*]\n    evidence: true\n"), 0o644)
	s := Take(root, []string{"ORDERS_APPLY=1", "OTHER=1"})
	why := s.Unapprovable("ORDERS_APPLY")
	if !strings.Contains(why, "lookup.yml rule for ORDERS_APPLY requires evidence-bound approvals, which swim no longer has: remove `evidence: true`") {
		t.Fatalf("unapprovable: %q", why)
	}
	if r := s.Signed(Ask{Root: root, Lane: 1, Flag: "ORDERS_APPLY", Script: "abc"}, time.Now()); r.OK || r.Why != why {
		t.Errorf("signed: %+v", r)
	}
	if s.Unapprovable("OTHER") != "" {
		t.Error("OTHER is unapprovable")
	}
	bad := &Snapshot{LookupErr: errors.New("lookup.yml: broken")}
	if bad.Unapprovable("ORDERS_APPLY") != "" || bad.Signed(Ask{Flag: "ORDERS_APPLY"}, time.Now()).Why != "lookup.yml: broken" {
		t.Error("a lookup.yml that doesn't load")
	}
}
