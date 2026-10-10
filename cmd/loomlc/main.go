// Command loomlc runs tasks through lifecycles of agent steps.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/stritech-oss/loomlc/internal/cli"
	"github.com/stritech-oss/loomlc/internal/proc"
)

func main() {
	// A signal cancels the context rather than ending the process, so a run in flight releases its claim
	// and its workspace instead of leaving a task marked as being worked on by nobody.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Stop handling signals once one has arrived, so a second one ends the process rather than being
	// swallowed while a run is letting go of what it holds.
	go func() {
		<-ctx.Done()
		stop()
	}()

	dir, err := os.Getwd()
	if err != nil {
		_, _ = os.Stderr.WriteString("loomlc: " + err.Error() + "\n")
		os.Exit(cli.ExitFailure)
	}
	os.Exit(cli.Main(ctx, os.Args[1:], cli.Env{
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		ReadFile: os.ReadFile,
		LookPath: proc.Exec{}.LookPath,
		Environ:  os.Environ,
		Runner:   proc.Exec{},
		Dir:      dir,
	}))
}
