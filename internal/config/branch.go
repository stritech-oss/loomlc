package config

import (
	"fmt"
	"strings"
)

// maxSlug bounds the readable half of a branch name, so a long title doesn't become a branch nobody can
// type.
const maxSlug = 50

// untitled stands in when a title has nothing a branch name can use, such as a title written entirely in
// a script with no ASCII letters.
const untitled = "task"

// BranchName renders a lifecycle's branch template for one task. The template was checked when the
// configuration loaded; this checks the name the task's own title produced.
func BranchName(tmpl, id, title string) (string, error) {
	name, err := renderBranch(tmpl, id, slug(title))
	if err != nil {
		return "", fmt.Errorf("the branch template %q: %w", tmpl, err)
	}
	if problem := branchProblem(name); problem != "" {
		return "", fmt.Errorf("the branch template %q renders %q for %s: %s", tmpl, name, id, problem)
	}
	return name, nil
}

// slug is a task's title as a branch name's readable half: lowercase, with every run of other characters
// a single hyphen.
func slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxSlug {
		// Cut at the last whole word, so a branch name doesn't end in half of one.
		cut := s[:maxSlug]
		if i := strings.LastIndexByte(cut, '-'); i > 0 {
			cut = cut[:i]
		}
		s = strings.Trim(cut, "-")
	}
	if s == "" {
		return untitled
	}
	return s
}
