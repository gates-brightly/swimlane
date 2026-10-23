package e2e

import (
	"strings"
	"testing"
)

// On a headless worker a guard no one can ever answer fails its round,
// BLOCKED with why, instead of dry-running the change unseen: one its
// script doesn't declare, and one the hub can't answer (no lookup.yml).
func TestHeadlessUnanswerableGuardFails(t *testing.T) {
	long(t)
	r, _ := headlessRepo(t)
	r.script(1, "renew", `if guard RENEW "renew the certs"; then run "renew" echo renewed; fi`)
	withHeader(t, r, 1, "# Guards:  RENEW renew the certs")
	r.script(2, "undeclared", `F=WI; F=${F}PE; if guard "$F" "wipe the cache"; then run "wipe" echo wiped; fi`)
	withHeader(t, r, 2, "# Guards:  RENEW renew the certs")
	r.mustSwim("watch", "--headless", "--detach", "--interval", "200ms")
	t.Cleanup(func() { r.swim("watch", "--headless", "--stop") })

	r.mustSwim("queue", "add", "1")
	waitDone(t, r, 1)
	if st := r.status().Get(1).State; st != "failed" {
		t.Errorf("lane 1 with no one to answer: %s\n%s", st, r.log(1))
	}
	contains(t, "lane 1", r.log(1), "BLOCKED  renew the certs (can't be answered here: no one may answer it through the hub", "STOP  guard RENEW can't be answered on this swimmer")
	if strings.Contains(r.log(1), "renewed") {
		t.Errorf("lane 1 renewed without an answer:\n%s", r.log(1))
	}

	r.mustSwim("queue", "add", "2")
	waitDone(t, r, 2)
	if st := r.status().Get(2).State; st != "failed" {
		t.Errorf("lane 2's undeclared guard: %s\n%s", st, r.log(2))
	}
	contains(t, "lane 2", r.log(2), "BLOCKED  wipe the cache (can't be answered here:")
}
