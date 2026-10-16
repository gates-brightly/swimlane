package lane

import (
	"strings"
	"testing"
)

// Probes come from the header; so do guards, whatever the body calls.
func TestObserveHeaderAndGuards(t *testing.T) {
	info := ParseScript(`#!/usr/bin/env python3
# Round: r
# Guards: ALLOW_X  do x
# Guards: *  a flag named at run time
# Observe: acct = aws sts get-caller-identity --query Account --output text
# Observe: path terraform aws
# Observe: nonsense
import swim
with swim.round() as r:
    if r.guard("ALLOW_X", "do x"):
        pass
    r.guard(flag, "dynamic")
`)
	if len(info.Observe) != 2 || info.Observe[0].Name != "acct" || strings.Join(info.Observe[1].Names(), ",") != "path.terraform,path.aws" || info.Observe[0].Line != 5 {
		t.Errorf("probes: %+v", info.Observe)
	}
	if len(info.Problems) != 1 || !strings.HasPrefix(info.Problems[0], "Observe:") {
		t.Errorf("problems: %v", info.Problems)
	}
	if strings.Join(info.GuardFlags(), ",") != "ALLOW_X" || info.DynamicGuards != 1 {
		t.Errorf("guards: %v, dynamic %d", info.GuardFlags(), info.DynamicGuards)
	}
}
