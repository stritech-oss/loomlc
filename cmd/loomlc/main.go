// Command loomlc runs tasks through lifecycles of agent steps.
package main

import (
	"os"

	"github.com/stritech-oss/loomlc/internal/cli"
	"github.com/stritech-oss/loomlc/internal/proc"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.Env{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		ReadFile: os.ReadFile,
		LookPath: proc.Exec{}.LookPath,
	}))
}
