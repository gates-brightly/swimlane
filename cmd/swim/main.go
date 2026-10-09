// Command swim runs operator-in-the-loop automation lanes. See `swim --help`.
package main

import (
	"os"

	"github.com/gates-brightly/swimlane/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
