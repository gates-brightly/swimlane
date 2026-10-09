// Command swim runs operator-in-the-loop automation lanes. See `swim --help`.
package main

import (
	"os"

	"swim/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
