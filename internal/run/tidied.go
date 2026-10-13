package run

import (
	"fmt"
	"strings"

	"github.com/stritech-oss/loomlc/internal/git"
	"github.com/stritech-oss/loomlc/internal/sink"
)

// Tidied is the comment loomlc posts after folding a pull request's fixup commits: the history as the
// reviewer read it, the history as it is now, and what that means for anyone holding a copy.
//
// It carries a marker of loomlc's own, so a later feedback run doesn't read these words back as a
// reviewer's.
func Tidied(t Tidy) string {
	var b strings.Builder
	b.WriteString(sink.TidyMarker + " -->\n")

	if !t.Folded() {
		b.WriteString("## loomlc couldn't fold the fixups on this branch\n\n")
		b.WriteString("Nothing was pushed, so the branch is as it was — and the Commit policy check stays red" +
			" while a `fixup!` commit is on it.\n")
		writeItems(&b, "Why", t.Refusals)
		// The commit, not the base branch: rebasing onto the tip of the base branch would move the
		// branch forward as well as folding it, which changes the tree and is what tidy refuses.
		fmt.Fprintf(&b, "\nFolding by hand is `git rebase -i --autosquash %s` on the branch.\n", short(t.Base))
		return b.String()
	}

	b.WriteString("## loomlc folded the fixups on this branch\n\n")
	fmt.Fprintf(&b, "%s The tree is byte-identical, so the branch still adds up to exactly what was"+
		" reviewed.\n", intoPlace(t.Fixups))
	writeCommits(&b, "Before", t.Before)
	writeCommits(&b, "Now", t.After)
	b.WriteString("\nThe branch was force-pushed, so a local copy of it has to be reset to it rather than pulled." +
		" Review comments stay on the pull request, though GitHub may mark some as outdated.\n")
	return b.String()
}

// intoPlace says what the fold moved. git matches a fixup to its target by subject, so where two
// commits on a branch share one this says what went in, not which commit it landed in.
func intoPlace(n int) string {
	if n == 1 {
		return "One fix went into the commit it belonged to."
	}
	return fmt.Sprintf("%d fixes went into the commits they belonged to.", n)
}

// writeCommits lists a history one commit per line, flattening each subject: it is an agent's words, and
// on a branch with other people's commits it is a stranger's.
func writeCommits(b *strings.Builder, title string, commits []git.Commit) {
	if len(commits) == 0 {
		return
	}
	fmt.Fprintf(b, "\n**%s**\n\n", title)
	for _, c := range commits {
		fmt.Fprintf(b, "- `%s` %s\n", short(c.SHA), oneLine(c.Subject))
	}
}
