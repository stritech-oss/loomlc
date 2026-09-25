package commit

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// maxSubject is the header limit docs/contributing.md sets.
const maxSubject = 72

// header is the Conventional Commit form scripts/commit-policy.sh enforces, checked here so an agent
// hears about it in findings rather than from a hook after the commit.
var header = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: [^ ]`)

// Existing is a commit already on the branch, which a fix can name. The caller fills these from the git
// log, so this package needs no git of its own.
type Existing struct {
	SHA     string
	Subject string
}

// fix reports whether this commit is itself a fix waiting to be folded.
func (e Existing) fix() bool {
	return strings.HasPrefix(e.Subject, "fixup! ") || strings.HasPrefix(e.Subject, "squash! ")
}

// Proposed is one commit a change step asked loomlc to make.
type Proposed struct {
	// Message is a Conventional Commit for new work, or the explanation when Fixes is set.
	Message string
	// Paths are the files this commit covers, relative to the repository root.
	Paths []string
	// Fixes names the commit on this branch this one fixes. Empty means new work.
	Fixes string
}

// Finding is one reason loomlc can't make the commits as asked. The change step gets these back and
// answers them, so each one says what to do differently.
type Finding struct {
	// Commit is which proposed commit this is about, counting from 1. Zero means the set as a whole.
	Commit int
	Path   string
	Detail string
}

// Check reports why loomlc can't make these commits, given the paths the agent changed and the commits
// already on the branch. No findings means the engine can go ahead.
func Check(proposed []Proposed, changed []string, branch []Existing) []Finding {
	var findings []Finding
	add := func(n int, p, format string, args ...any) {
		findings = append(findings, Finding{Commit: n, Path: p, Detail: fmt.Sprintf(format, args...)})
	}

	if len(proposed) == 0 {
		if len(changed) > 0 {
			add(0, "", "%d files changed but no commits were proposed; describe the commits to make, or revert what you changed", len(changed))
		}
		return findings
	}

	covered := map[string]int{} // path to the commit that claims it
	for i, p := range proposed {
		n := i + 1
		if len(p.Paths) == 0 {
			add(n, "", "has no paths; every commit names the files it covers")
		}
		for _, raw := range p.Paths {
			clean, err := relative(raw)
			if err != nil {
				add(n, raw, "%v", err)
				continue
			}
			if other, ok := covered[clean]; ok {
				add(n, clean, "is already in commit %d; git can't split one file's changes across two commits, so put it in one and explain both parts", other)
				continue
			}
			covered[clean] = n
			if !slices.Contains(changed, clean) {
				add(n, clean, "has nothing to commit; it isn't among the files you changed")
			}
		}
		checkFixes(add, n, p, branch)
	}

	for _, c := range changed {
		if _, ok := covered[c]; !ok {
			add(0, c, "changed but no commit covers it; add it to a commit or undo the change")
		}
	}
	checkSubjects(add, proposed, branch)
	return findings
}

// checkFixes checks a fix names a commit the engine can fold it into, or that new work is described by a
// message the policy accepts.
func checkFixes(add func(int, string, string, ...any), n int, p Proposed, branch []Existing) {
	if p.Fixes == "" {
		checkMessage(add, n, p.Message)
		return
	}
	if strings.TrimSpace(p.Message) == "" {
		add(n, "", "fixes %s without saying what was wrong; the explanation is what a reviewer reads", short(p.Fixes))
	}

	var matches []Existing
	for _, c := range branch {
		if strings.HasPrefix(c.SHA, p.Fixes) {
			matches = append(matches, c)
		}
	}
	switch {
	case len(p.Fixes) < 7:
		add(n, "", "fixes %q, which is too short to name a commit; use at least seven characters of a SHA from the branch's commit list", p.Fixes)
	case len(matches) == 0:
		add(n, "", "fixes %s, which isn't a commit on this branch; only the commits listed in your prompt can be fixed", short(p.Fixes))
	case len(matches) > 1:
		add(n, "", "fixes %s, which matches %d commits on this branch; use more of the SHA", short(p.Fixes), len(matches))
	case matches[0].fix():
		add(n, "", "fixes %s, which is itself a fix; point at the commit it names instead", short(p.Fixes))
	}
}

// checkMessage holds a new commit's message to the rules the policy check enforces after the fact.
func checkMessage(add func(int, string, string, ...any), n int, message string) {
	subject, body, _ := strings.Cut(message, "\n")
	switch {
	case strings.TrimSpace(message) == "":
		add(n, "", "has no message")
		return
	case !header.MatchString(subject):
		add(n, "", "the message %q isn't a Conventional Commit; write type(scope): subject, such as feat(cli): print the build commit", subject)
	case len(subject) > maxSubject:
		add(n, "", "the message is %d characters; the limit is %d", len(subject), maxSubject)
	case strings.HasSuffix(subject, "."):
		add(n, "", "the message ends with a period")
	}
	if body != "" && !strings.HasPrefix(body, "\n") {
		add(n, "", "needs a blank line between the subject and the body")
	}
}

// checkSubjects reports two commits that would share a subject. Folding a fix finds its target by
// subject, so a duplicate makes that ambiguous for every fix that follows.
func checkSubjects(add func(int, string, string, ...any), proposed []Proposed, branch []Existing) {
	seen := map[string]bool{}
	for _, c := range branch {
		if !c.fix() {
			seen[c.Subject] = true
		}
	}
	for i, p := range proposed {
		if p.Fixes != "" {
			continue
		}
		subject, _, _ := strings.Cut(p.Message, "\n")
		if seen[subject] {
			add(i+1, "", "the message %q is already the subject of another commit; folding a fix finds its target by subject, so each one has to be different", subject)
		}
		seen[subject] = true
	}
}

// relative returns the path as git will see it, or says why it can't be committed.
func relative(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("is an empty path")
	}
	if path.IsAbs(p) || strings.HasPrefix(p, `\`) || strings.Contains(p, `:\`) {
		return "", fmt.Errorf("%q is an absolute path; commit paths are relative to the repository", p)
	}
	clean := path.Clean(strings.ReplaceAll(p, `\`, "/"))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%q is outside the repository", p)
	}
	return clean, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
