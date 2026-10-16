// Command conformance is the lane-protocol conformance round in Go: the
// same steps as the bash, Python and TypeScript versions in the e2e tests,
// which compare their logs.
package main

import (
	"os"

	"github.com/gates-brightly/swimlane/sdk/go/swim"
)

func main() {
	code := swim.Round(func(r *swim.R) error {
		defer os.WriteFile("finally.go.txt", nil, 0o644)
		if err := r.Stage("check"); err != nil {
			return err
		}
		if _, err := r.Run("probe", "echo", "probing"); err != nil {
			return err
		}
		if err := r.Step("native check", false, func(s *swim.S) error {
			s.Log("items: 0")
			return nil
		}); err != nil {
			return err
		}
		if _, err := r.RunWith("refused", swim.Opts{ExpectFail: true, ExpectCodes: []int{3}}, "sh", "-c", "exit 3"); err != nil {
			return err
		}
		ok, err := r.Guard("ALLOW_X", "do x")
		if err != nil {
			return err
		}
		if ok {
			if _, err := r.Run("x", "true"); err != nil {
				return err
			}
		}
		if err := r.Msg("almost done", ""); err != nil {
			return err
		}
		if err := r.Stage("change"); err != nil {
			return err
		}
		if _, err := r.Gate("must pass", "false"); err != nil {
			return err
		}
		_, err = r.Run("never", "true")
		return err
	})
	os.Exit(code)
}
