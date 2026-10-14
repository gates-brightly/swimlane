package logparse

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzParse feeds the lane log parser arbitrary bytes: it never panics,
// and what it reports holds together (a finished round was found; a
// round with no END has no exit code to trust). Seeds: real logs in
// testdata/fuzz/FuzzParse and the ones below.
func FuzzParse(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("# swim lane log | syntax 2 | swim 1\n\n== ROUND 2026-01-02T03:04:05Z  job=j  title\n   run: r-1 | script: lane.1.sh\n  PASS  a                                   0.0s  03:04:05\n        $ true\n\n== END PASS  pass=1 fail=0 skip=0 drift=0  exit=0  0.1s  2026-01-02T03:04:05Z  run=r-1\n"))
	f.Add([]byte("== ROUND x\n  FAIL  b (exit 2)  0.1s  00:00:00\n        | out\n  -- MSG hi\n== END"))
	f.Add([]byte("2025-01-01 swim 3 START\n  PASS  old style\n2025-01-01 swim 3 END exit=0\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Parse(bytes.NewReader(data))
		if err != nil || r == nil {
			return
		}
		if r.Finished && !r.Found {
			t.Errorf("a finished round that wasn't found: %+v", r)
		}
		for _, l := range strings.Split(string(data), "\n") {
			ParseContext(l)
		}
		r.StageResults()
		r.HandlerResults()
		r.Failed()
	})
}
