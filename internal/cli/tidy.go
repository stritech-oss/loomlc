package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/stritech-oss/loomlc/internal/app"
	"github.com/stritech-oss/loomlc/internal/run"
	"github.com/stritech-oss/loomlc/internal/sink"
)

const tidyUsage = `Usage: loomlc tidy [flags] <output>

Folds the fixup commits a feedback run left on one pull request into the commits they fix, and
force-pushes the result under a lease. This is the pre-merge step a person runs: it rewrites
commits a reviewer has already read, which nothing in a run does. The output is named by its id in
the lifecycle's sink, such as 44 for a GitHub pull request.

The fold is refused unless the tree comes out byte-identical, so the branch keeps exactly the
changes that were reviewed, and the folded commits then go through the commit policy with nothing
tolerated — the check the fixups were holding up. A fold that doesn't apply is said on the pull
request and left for you, since the conflict is yours to resolve. loomlc won't fold a pull request
that is closed or comes from a fork, and it says nothing on one it changed nothing about.

Exit codes:
  0  the fixups were folded and pushed, or there were none to fold
  1  the branch wasn't left ready to merge: loomlc wouldn't fold this pull request, the fixups
     didn't fold, what came out didn't pass the commit policy, or the command failed — including
     after a successful push, which the output says. The reasons are on the pull request or on
     stderr.
  2  the command line was wrong

`

func runTidy(ctx context.Context, args []string, env Env) int {
	flags := flag.NewFlagSet("loomlc tidy", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	// flag prints its own copy of the usage on any error, including help that was asked for. Help goes
	// to stdout below, where it can be paged, and an error is reported by this command.
	flags.Usage = func() {}
	usage := func(w io.Writer) int {
		_, _ = io.WriteString(w, tidyUsage)
		flags.SetOutput(w)
		flags.PrintDefaults()
		return ExitOK
	}
	configPath := flags.String("config", "", "read the configuration from `path` instead of ./"+defaultConfigFile)
	name := flags.String("lifecycle", "", "use the lifecycle called `name`, when the configuration has more than one")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usage(env.Stdout)
		}
		return ExitUsage
	}
	// Folding is never implied: it rewrites a branch, so it acts on the pull request it was told to and
	// never on whichever one came up next.
	if flags.NArg() != 1 {
		return fail(env.Stderr, fmt.Sprintf("loomlc tidy: name one pull request to fold, and %q isn't one\n", flags.Args()))
	}
	if err := checkConfigFlag(flags); err != nil {
		return fail(env.Stderr, fmt.Sprintf("loomlc tidy: %v\n", err))
	}
	// An output can be given as it is printed — github-pr#44 — as well as by its bare id.
	named, id, printed := strings.Cut(flags.Arg(0), "#")
	if !printed {
		named, id = "", named
	}
	if !taskID.MatchString(id) {
		return fail(env.Stderr, fmt.Sprintf("loomlc tidy: %q isn't an output id: letters, digits, dots, hyphens and underscores\n", id))
	}

	a, lc, code := build(ctx, env, "tidy", *configPath, *name, app.MovesCommits)
	if code != ExitOK {
		return code
	}
	if lifecycleSink := lc.Config.Sink; named != "" && named != lifecycleSink {
		return fail(env.Stderr, fmt.Sprintf("loomlc tidy: this lifecycle publishes to %q, not %q\n", lifecycleSink, named))
	}

	res, err := a.Tidier(lc, env.Stdout).Tidy(ctx, sink.Ref{Sink: lc.Config.Sink, ID: id})
	// A failed write to stdout can't be reported anywhere useful, and the exit code has to describe the
	// fold rather than the pipe.
	_, _ = io.WriteString(env.Stdout, describeTidy(res))
	if err != nil {
		return failure(env.Stderr, fmt.Sprintf("loomlc tidy: %v\n", err))
	}
	if res.Idle == "" && !res.Folded() {
		return ExitFailure // the reasons are on the pull request, and in what was just printed
	}
	return ExitOK
}

// describeTidy says how the fold ended, after the lines it printed as it went.
func describeTidy(res run.Tidy) string {
	var b strings.Builder
	switch {
	case res.Idle != "":
		return "Nothing to do: " + res.Idle + ".\n"
	case res.Output.ID == "":
		return "" // no pull request was looked at, and the error says why
	case res.Folded():
		fmt.Fprintf(&b, "Folded %s into the %s on %s.\n", fixes(res.Fixups), commits(len(res.After)), res.URL)
		b.WriteString("  The branch was force-pushed, so a local copy of it has to be reset rather than pulled.\n")
		if !res.Commented {
			// The branch has moved and the pull request wasn't told, which is the one ending where the
			// exit code's "not ready to merge" and what happened don't line up.
			fmt.Fprintf(&b, "  Nothing was said on %s about it, though.\n", res.Output)
		}
	default:
		fmt.Fprintf(&b, "Nothing was folded on %s, so its fixups are still there.\n", res.Output)
		if len(res.Refusals) > 0 {
			b.WriteString("Why:\n" + bullets(res.Refusals))
		}
		if res.Fixups > 0 && !res.Commented {
			fmt.Fprintf(&b, "Nothing was said on %s either.\n", res.Output)
		}
	}
	return b.String()
}

func fixes(n int) string {
	if n == 1 {
		return "1 fix"
	}
	return fmt.Sprintf("%d fixes", n)
}
