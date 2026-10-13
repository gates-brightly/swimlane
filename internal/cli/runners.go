package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gates-brightly/swimlane/internal/ci"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/runner"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// confirmRunners asks the operator, once per extension, which command runs
// the lane scripts about to run that have no #! line and no configured
// runner (lane.2.ts with no runners: ts), and saves each answer in this
// repo's section of the config. Off a terminal (CI, a queue) it asks
// nothing: such a lane fails at start with the exact fix. lanes empty means
// every pending lane.
func confirmRunners(root string, cfg *config.Config, lanes []int) (*config.Config, error) {
	if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stderr) || ci.InCI(os.Getenv) {
		return cfg, nil
	}
	if len(lanes) == 0 {
		for n := 1; n <= cfg.Lanes; n++ {
			if info, err := lane.ReadScript(root, n); err == nil && info.Pending() {
				lanes = append(lanes, n)
			}
		}
	}
	asked := map[string]bool{}
	for _, n := range lanes {
		_, err := runner.Resolve(cfg, lane.Script(root, n))
		var need *runner.NeedRunner
		if !errors.As(err, &need) || asked[need.Ext] {
			continue
		}
		asked[need.Ext] = true
		cmd, err := runner.Confirm(os.Stdin, os.Stderr, need)
		if errors.Is(err, runner.ErrDeclined) {
			return nil, fmt.Errorf("%s: no runner chosen for .%s lanes; nothing ran.\n%s", filepath.Base(need.Script), need.Ext, need.Error())
		}
		if err != nil {
			return nil, err
		}
		if err := runner.Save(root, need.Ext, cmd); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "swim: .%s lanes run with %s (saved as runners.%s in %s; change it: swim config runners.%s CMD)\n", need.Ext, cmd, need.Ext, config.Path(), need.Ext)
		if cfg, err = config.Load(root); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}
