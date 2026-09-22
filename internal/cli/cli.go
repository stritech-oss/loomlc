// Package cli parses the loomlc command line and dispatches subcommands.
package cli

import (
	"fmt"
	"io"

	"github.com/stritech-oss/loomlc/internal/version"
)

// Exit codes returned by Main.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Env is what commands use from outside the process, so they can be tested without touching it.
type Env struct {
	Stdout, Stderr io.Writer
	// ReadFile reads the named file. main passes os.ReadFile.
	ReadFile func(name string) ([]byte, error)
	// LookPath finds an executable in PATH. main passes proc.Exec{}.LookPath.
	LookPath func(name string) (string, error)
}

const usage = `loomlc runs tasks through lifecycles of agent steps.

Usage:
  loomlc <command> [arguments]

Commands:
  lifecycles  print the lifecycles in the resolved configuration
  providers   print the configured providers and what their adapters support
  version     print the loomlc version
  help        print this help
`

// Main runs the command named by args and returns the process exit code.
func Main(args []string, env Env) int {
	if len(args) == 0 {
		return fail(env.Stderr, usage)
	}

	switch cmd, rest := args[0], args[1:]; cmd {
	case "lifecycles":
		return runLifecycles(rest, env)
	case "providers":
		return runProviders(rest, env)
	case "version":
		return runVersion(rest, env)
	case "help", "-h", "--help":
		return print(env.Stdout, usage)
	default:
		return fail(env.Stderr, fmt.Sprintf("loomlc: unknown command %q\n\n%s", cmd, usage))
	}
}

func runVersion(args []string, env Env) int {
	if len(args) > 0 {
		return fail(env.Stderr, fmt.Sprintf("loomlc version: unexpected arguments %q\n", args))
	}
	return print(env.Stdout, fmt.Sprintf("loomlc %s\n", version.Version))
}

// print writes msg to w and reports success. A failed write to stdout can't be reported anywhere
// useful, so it only affects the exit code.
func print(w io.Writer, msg string) int {
	if _, err := io.WriteString(w, msg); err != nil {
		return ExitFailure
	}
	return ExitOK
}

// fail reports a command that was invoked wrongly: the operator has to change what they typed.
func fail(w io.Writer, msg string) int {
	_, _ = io.WriteString(w, msg) // best effort: the exit code already reports the failure
	return ExitUsage
}

// failure reports a command that was invoked correctly but couldn't do its job, such as a configuration
// that doesn't load. A script can tell the two apart by the exit code.
func failure(w io.Writer, msg string) int {
	_, _ = io.WriteString(w, msg)
	return ExitFailure
}
