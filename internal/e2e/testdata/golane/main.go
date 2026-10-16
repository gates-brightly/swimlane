// Command golane is a compiled Go SDK lane for the e2e tests: it calls a
// module its sidecar declares (lane.N.yml modules:), takes a module's
// variables with ModuleEnv, and runs a gate module that fails.
package main

import (
	"os"

	"github.com/gates-brightly/swimlane/sdk/go/swim"
)

func main() {
	os.Exit(swim.Round(func(r *swim.R) error {
		if _, err := r.Module("kit", "hello", "from", "go"); err != nil {
			return err
		}
		if _, err := r.ModuleEnv("kit_env"); err != nil {
			return err
		}
		if _, err := r.Run("see", "bash", "-c", "echo region=$KIT_REGION"); err != nil {
			return err
		}
		if _, err := r.Run("own env", "echo", "mine="+os.Getenv("KIT_REGION")); err != nil {
			return err
		}
		if _, err := r.Module("kitgate"); err != nil {
			return err // the gate module failed: the round ended
		}
		_, err := r.Run("never", "echo", "should-not-run")
		return err
	}))
}
