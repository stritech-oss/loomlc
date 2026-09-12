// Command loomlc runs tasks through lifecycles of agent steps.
package main

import (
	"os"

	"github.com/stritech-oss/loomlc/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
