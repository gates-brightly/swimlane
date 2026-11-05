package authority

import "testing"

// Guard decides as swim run does as it starts: a flag set in the
// environment approves; otherwise swim run asks.
func TestGuard(t *testing.T) {
	if g := Take(nil).Guard("DROP_OK", "drop table old_orders"); g.Approved || !g.Asks {
		t.Errorf("unset: %+v", g)
	} else if got := g.Text(); got != "not approved; swim run will ask (a dry run if no one answers)" {
		t.Errorf("unset text: %q", got)
	}
	if g := Take([]string{"DROP_OK=1"}).Guard("DROP_OK", ""); !g.Approved || g.Text() != "approved: DROP_OK=1" {
		t.Errorf("flag: %+v %q", g, g.Text())
	}
	q := Take([]string{"DROP_OK=1", "SWIM_QUEUE=q-1", "SWIM_QUEUE_APPROVED=DROP_OK"}).FlagSet("DROP_OK")
	if !q.OK || q.Entry.Method != "queue" || q.How != "DROP_OK=1, approved in q-1" {
		t.Errorf("queued: %+v", q)
	}
	if r := Take(nil).FlagSet("DROP_OK"); r.OK || r.Why == "" {
		t.Errorf("set inside the lane: %+v", r)
	}
}
