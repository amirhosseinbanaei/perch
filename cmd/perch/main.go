// Command perch lists and manages tmux sessions and the coding agents running in them.
package main

import (
	"os"

	"github.com/amirhosseinbanaei/perch/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], cli.OSEnv()))
}
