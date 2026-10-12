package run

import (
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/lifecycle"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// Reply is the comment loomlc posts on a pull request after a feedback run: what it did about each
// comment, in its own words, what that put on the branch, and what a person has to do next.
//
// It carries loomlc's reply marker, which is where "since the last reply" starts, so the next feedback
// run doesn't read these words back as a reviewer's.
func Reply(rev Revision) string {
	var b strings.Builder
	b.WriteString(sink.ReplyMarker + " -->\n")
	b.WriteString("## loomlc answered this review\n\n")
	b.WriteString(answer(rev) + "\n")

	if len(rev.Addressed) > 0 {
		b.WriteString("\n**Point by point**\n\n")
		for _, a := range rev.Addressed {
			item := oneLine(a.Item)
			if item == "" {
				item = "A comment it didn't name"
			}
			// Both halves are flattened: a reviewer's words and an agent's answer are rendered into one
			// bullet, and either could otherwise add bullets of its own that nobody wrote.
			fmt.Fprintf(&b, "- **%s** — %s\n", item, oneLine(a.Response))
		}
	}

	if rev.Pushed && len(rev.Commits) > 0 {
		b.WriteString("\n**Pushed**\n\n")
		for _, c := range rev.Commits {
			fmt.Fprintf(&b, "- `%s` %s\n", short(c.SHA), oneLine(c.Subject))
		}
	}
	writeItems(&b, "What stopped it", rev.Blockers)
	writeItems(&b, "Why it wasn't pushed", rev.Refusals)
	writeItems(&b, "Worth knowing", rev.Warnings)

	if rev.Pushed && rev.Fixups > 0 {
		fmt.Fprintf(&b, "\n%s Fold them before merging — `git rebase -i --autosquash %s` on the branch — because"+
			" the Commit policy check stays red while a `fixup!` commit is on it.\n", leftStanding(rev.Fixups), rev.Base)
	}
	if rev.Log != "" {
		fmt.Fprintf(&b, "\nThe prompts and answers are in `%s`.\n", rev.Log)
	}
	return b.String()
}

// answer says in one line how the revision ended, since what follows it is an agent's words rather than
// loomlc's.
func answer(rev Revision) string {
	switch {
	case rev.Outcome == 0 && len(rev.Addressed) == 0 && len(rev.Blockers) == 0:
		return "There was nothing here loomlc hadn't already answered, so the labels are cleared."
	case rev.Outcome == 0:
		return "loomlc couldn't revise this change."
	case len(rev.Refusals) > 0:
		return "The revision passed review, but loomlc wouldn't push it."
	case rev.Outcome == lifecycle.Blocked:
		return "A step stopped: it couldn't make this change."
	case rev.Outcome == lifecycle.Exhausted:
		return fmt.Sprintf("The revision didn't pass review in %s, so nothing was pushed.", passes(rev.Passes))
	case !rev.Pushed:
		return "Nothing on the branch needed to change. The answers are below."
	default:
		return "The branch has been updated."
	}
}

// leftStanding says why the fixups are still there, since a reviewer who sees one may reasonably think
// the run forgot to fold it.
func leftStanding(n int) string {
	if n == 1 {
		return "One of those is a `fixup!`: a fix to a commit you have already read, left standing rather than folded into it."
	}
	return fmt.Sprintf("%d of those are `fixup!`s: fixes to commits you have already read, left standing rather than folded into them.", n)
}

// short abbreviates a SHA the way git does, and leaves anything shorter alone.
func short(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

// oneLine flattens text that goes into a list item, so a reviewer's own words can't add lines of their
// own to the comment.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
