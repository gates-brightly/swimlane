package cli

import "testing"

// Who decides a guard, for every way a lane's process can be started. The
// properties: swim run's launcher decides whatever else holds; under swim
// run without its channel, the lane's own FLAG=1 never approves; only a
// lane run directly with bash takes FLAG=1; and the process tree is walked
// only when it matters.
func TestGuardRoute(t *testing.T) {
	for _, launched := range []bool{false, true} {
		for _, underRun := range []bool{false, true} {
			for _, flagSet := range []bool{false, true} {
				walked := false
				got := guardRoute(launched, func() bool { walked = true; return underRun }, flagSet)
				var want route
				switch {
				case launched:
					want = routeLauncher
				case underRun:
					want = routeRefuse
				case flagSet:
					want = routeFlag
				default:
					want = routeSelf
				}
				if got != want {
					t.Errorf("launched %v, under swim run %v, flag set %v: route %d, want %d", launched, underRun, flagSet, got, want)
				}
				if got == routeFlag && (launched || underRun) {
					t.Errorf("a lane under swim run approved itself with FLAG=1 (launched %v)", launched)
				}
				if launched && walked {
					t.Error("walked the process tree for a launched lane")
				}
			}
		}
	}
}
