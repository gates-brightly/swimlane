// Package failpoint stops swim at chosen points, so the fault tests
// (internal/e2e/fault_test.go) can crash it exactly there and check what a
// restart makes of the state it left.
//
// The hooks are compiled in only with the build tag swimfailpoint (go build
// -tags swimfailpoint): a normal build has Hit and Armed as empty functions,
// and reads no environment for them. With the tag, SWIM_FAILPOINT names the
// sites to act at, each with what to do there:
//
//	SWIM_FAILPOINT=step.after-exit:pause,chain.mid-append:kill
//
//	kill    the process SIGKILLs itself (the default)
//	pause   the process stops there for good, for the test to kill whatever
//	        it chooses (the whole worker: swim run, the lanes, their steps)
//
// With SWIM_FAILPOINT_DIR, a mode may end in @N (kill@3): act at the Nth
// time any process gets there, not the first.
//
// SWIM_FAILPOINT_DIR, when set, makes each site act once across every
// process that inherits the variable (and counts the hits for @N): the one
// that acts creates DIR/<site>.hit (holding its pid) first; later ones
// pass. Tests wait for that file to know the process got there. Without
// it, every process that gets there acts.
//
// The sites:
//
//	step.after-exit    swim step: the command exited, its PASS/FAIL line not yet written
//	queue.claimed      a queue entry renamed to .running, its worker not yet recorded
//	status.mid-write   half of status.yml's new temporary file written
//	chain.mid-append   half of a chain entry's line written and synced
package failpoint

// Sites the code defines (Hit and Armed take these names).
const (
	StepAfterExit  = "step.after-exit"
	QueueClaimed   = "queue.claimed"
	StatusMidWrite = "status.mid-write"
	ChainMidAppend = "chain.mid-append"
	EnvVar         = "SWIM_FAILPOINT"
	DirEnv         = "SWIM_FAILPOINT_DIR"
)
