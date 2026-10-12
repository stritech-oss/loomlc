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

const feedbackUsage = `Usage: loomlc feedback [flags] [output]

Answers the review on one open pull request: it reads what the reviewers wrote, revises the
change against it, pushes the result, and replies on the pull request point by point. Without an
output it takes the next one carrying the lifecycle's feedback label. The output is named by its
id in the lifecycle's sink, such as 44 for a GitHub pull request.

A fix to a commit a reviewer has already read is pushed as a fixup! commit, so they can see what
changed. Nothing is folded and nothing is force-pushed, which leaves the Commit policy check red
until a person folds them with git rebase -i --autosquash. Both feedback labels are cleared however
the revision ends, and the reply says what happened even when the revision broke.

Exit codes:
  0  the review was answered, or nothing was asking for changes
  1  the review wasn't answered — a step stopped, the review never passed, the push was refused,
     or the run itself failed. Either way the reasons are on the pull request or on stderr.
  2  the command line was wrong

`

func runFeedback(ctx context.Context, args []string, env Env) int {
	flags := flag.NewFlagSet("loomlc feedback", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	// flag prints its own copy of the usage on any error, including help that was asked for. Help goes
	// to stdout below, where it can be paged, and an error is reported by this command.
	flags.Usage = func() {}
	usage := func(w io.Writer) int {
		_, _ = io.WriteString(w, feedbackUsage)
		flags.SetOutput(w)
		flags.PrintDefaults()
		return ExitOK
	}
	configPath := flags.String("config", "", "read the configuration from `path` instead of ./"+defaultConfigFile)
	name := flags.String("lifecycle", "", "use the lifecycle called `name`, when the configuration has more than one")
	maxIter := flags.Int("max-iter", 0, "allow `n` passes of the change step, instead of the lifecycle's own limit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usage(env.Stdout)
		}
		return ExitUsage
	}
	if flags.NArg() > 1 {
		return fail(env.Stderr, fmt.Sprintf("loomlc feedback: one output at a time, and %q is more than one\n", flags.Args()))
	}
	if err := checkConfigFlag(flags); err != nil {
		return fail(env.Stderr, fmt.Sprintf("loomlc feedback: %v\n", err))
	}
	// An output can be given as it is printed — github-pr#44 — as well as by its bare id.
	named, id, printed := strings.Cut(flags.Arg(0), "#")
	if !printed {
		named, id = "", named
	}
	if id != "" && !taskID.MatchString(id) {
		return fail(env.Stderr, fmt.Sprintf("loomlc feedback: %q isn't an output id: letters, digits, dots, hyphens and underscores\n", id))
	}
	if *maxIter < 0 {
		return fail(env.Stderr, "loomlc feedback: --max-iter must be at least 1, or left out to use the lifecycle's own limit\n")
	}

	a, lc, code := build(ctx, env, "feedback", *configPath, *name)
	if code != ExitOK {
		return code
	}
	reviser, err := a.Reviser(lc, app.EngineOptions{MaxPasses: *maxIter, Progress: env.Stdout})
	if err != nil {
		return failure(env.Stderr, fmt.Sprintf("loomlc feedback: %v\n", err))
	}
	if lifecycleSink := lc.Config.Sink; named != "" && named != lifecycleSink {
		return fail(env.Stderr, fmt.Sprintf("loomlc feedback: this lifecycle publishes to %q, not %q\n", lifecycleSink, named))
	}

	ref := sink.Ref{}
	if id != "" {
		ref = sink.Ref{Sink: lc.Config.Sink, ID: id}
	}
	rev, err := reviser.Revise(ctx, ref)
	// A failed write to stdout can't be reported anywhere useful, and the exit code has to describe the
	// revision rather than the pipe.
	_, _ = io.WriteString(env.Stdout, describeRevision(rev))
	if err != nil {
		return failure(env.Stderr, fmt.Sprintf("loomlc feedback: %v\n", err))
	}
	if rev.Idle == "" && !rev.Answered() {
		return ExitFailure // the reasons are on the pull request, and in what was just printed
	}
	return ExitOK
}

// describeRevision says how the revision ended, after the lines it printed as it went.
func describeRevision(rev run.Revision) string {
	var b strings.Builder
	switch {
	case rev.Idle != "":
		return "Nothing to do: " + rev.Idle + ".\n"
	case rev.Answered():
		if !rev.Pushed {
			fmt.Fprintf(&b, "Answered the review on %s; nothing on the branch needed to change.\n", rev.URL)
			break
		}
		fmt.Fprintf(&b, "Answered the review on %s and pushed %s.\n", rev.URL, commits(len(rev.Commits)))
		if rev.Fixups > 0 {
			fmt.Fprintf(&b, "  %s still to fold: `git rebase -i --autosquash %s` on %s before merging.\n",
				commits(rev.Fixups), rev.Base, rev.Output)
		}
	case !rev.Claimed && len(rev.Blockers) == 0 && len(rev.Refusals) == 0:
		return "" // nothing was taken, and the error says why
	default:
		// Pushed but unanswered is the odd one: the branch moved and the reviewer wasn't told, so say
		// both rather than claiming the review was answered.
		if rev.Pushed {
			fmt.Fprintf(&b, "Pushed %s to %s.\n", commits(len(rev.Commits)), rev.Output)
		} else {
			fmt.Fprintf(&b, "Nothing was pushed to %s.\n", rev.Output)
		}
		if len(rev.Blockers) > 0 {
			b.WriteString("What stopped it:\n" + bullets(rev.Blockers))
		}
		if len(rev.Refusals) > 0 {
			b.WriteString("Why it wasn't pushed:\n" + bullets(rev.Refusals))
		}
		if !rev.Replied {
			fmt.Fprintf(&b, "Nothing was said on %s either.\n", rev.Output)
		}
	}
	for _, w := range rev.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	if rev.Log != "" {
		fmt.Fprintf(&b, "The run's prompts and answers are in %s\n", rev.Log)
	}
	return b.String()
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}
