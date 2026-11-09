package launcher

import (
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/authority"
	"github.com/gates-brightly/swimlane/internal/config"
)

// An answer from the phone is recorded as one: method phone, and where.
func TestDecidePhone(t *testing.T) {
	root := t.TempDir()
	a := &runAuthority{snap: authority.Take(nil), flags: map[string]bool{}, recorded: map[string]bool{}}
	o := Options{Root: root, Cfg: &config.Config{Root: root}, RunID: "r1"}
	ask := func(approval.Request, <-chan struct{}) approval.Response {
		return approval.Response{Approved: true, By: "zach", Via: "phone zphone"}
	}
	resp := a.decide(o, approval.Request{Lane: 3, Job: "deploy", Flag: "DEPLOY", RawDesc: "deploy 2.4.1"}, ask, nil)
	if !resp.Approved || resp.Grant == nil {
		t.Fatalf("response = %+v", resp)
	}
	e := resp.Grant.Entry
	if e.Method != "phone" || e.Statement != "approved by zach on phone zphone" || resp.How != e.Statement {
		t.Errorf("entry %+v, how %q", e, resp.How)
	}
	if !strings.HasSuffix(resp.Grant.History, "(DEPLOY, by zach on phone zphone)") {
		t.Errorf("history %q", resp.Grant.History)
	}
}
