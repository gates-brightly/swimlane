// Command secretlane is the Go lane of the secrets SDK test.
package main

import (
	"os"

	"github.com/gates-brightly/swimlane/sdk/go/swim"
)

func main() {
	os.Exit(swim.Round(func(r *swim.R) error {
		tok, err := swim.Secret.Get("api_token")
		if err != nil {
			return err
		}
		_, err = r.Run("use", "echo", "tok="+tok)
		return err
	}))
}
