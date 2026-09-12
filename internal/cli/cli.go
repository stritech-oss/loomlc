// Package cli parses the loomlc command line and dispatches subcommands.
package cli

import (
	"fmt"
	"io"

	"github.com/stritech-oss/loomlc/internal/version"
)

// Exit codes returned by Main.
const (
	ExitOK    = 0
	ExitUsage = 2
)

const usage = `loomlc runs tasks through lifecycles of agent steps.

Usage:
  loomlc <command> [arguments]

Commands:
  version    print the loomlc version
  help       print this help
`

// Main runs the command named by args and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, usage)
	}

	switch cmd, rest := args[0], args[1:]; cmd {
	case "version":
		return runVersion(rest, stdout, stderr)
	case "help", "-h", "--help":
		return print(stdout, usage)
	default:
		return fail(stderr, fmt.Sprintf("loomlc: unknown command %q\n\n%s", cmd, usage))
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		return fail(stderr, fmt.Sprintf("loomlc version: unexpected arguments %q\n", args))
	}
	return print(stdout, fmt.Sprintf("loomlc %s\n", version.Version))
}

// print writes msg to w and reports success. A failed write to stdout can't be reported anywhere
// useful, so it only affects the exit code.
func print(w io.Writer, msg string) int {
	if _, err := io.WriteString(w, msg); err != nil {
		return 1
	}
	return ExitOK
}

func fail(w io.Writer, msg string) int {
	_, _ = io.WriteString(w, msg) // best effort: the exit code already reports the failure
	return ExitUsage
}
