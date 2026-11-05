package lane

import (
	"strings"
	"testing"
)

// Guards come from the header, whatever the body calls.
func TestHeaderGuards(t *testing.T) {
	info := ParseScript(`#!/usr/bin/env python3
# Round: r
# Guards: ALLOW_X  do x
# Guards: *  a flag named at run time
import swim
with swim.round() as r:
    if r.guard("ALLOW_X", "do x"):
        pass
    r.guard(flag, "dynamic")
`)
	if strings.Join(info.GuardFlags(), ",") != "ALLOW_X" || info.DynamicGuards != 1 {
		t.Errorf("guards: %v, dynamic %d", info.GuardFlags(), info.DynamicGuards)
	}
}
