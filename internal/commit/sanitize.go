// Package commit turns the commits a change step asked for into commits loomlc can make, or into
// findings saying why it can't.
package commit

import (
	"regexp"
	"strings"
)

// attribution matches the lines scripts/commit-policy.sh rejects, which is the authority for this list.
// Sanitize drops them; TestSanitizedMessagesPassThePolicy keeps the two in step.
var attribution = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^[[:space:]]*co-authored-by:`),
	regexp.MustCompile(`(?i)^[[:space:]]*assisted-by:`),
	regexp.MustCompile(`(?i)^[[:space:]]*[a-z0-9-]*(session|thread|conversation|chat)[a-z0-9-]*:`),
	regexp.MustCompile(`(?i)^[[:space:]]*(🤖[[:space:]]*)?generated[ -](with|by|using)[[:space:]]`),
	regexp.MustCompile(`^[[:space:]]*🤖`),
	regexp.MustCompile(`(?i)^[[:space:]]*generated-(with|by|using):`),
	regexp.MustCompile(`(?i)claude\.ai/(code|chat|share)([/)>[:space:]]|$)`),
	regexp.MustCompile(`(?i)chatgpt\.com/(codex|c|share|g)([/)>[:space:]]|$)`),
	regexp.MustCompile(`(?i)chat\.openai\.com/`),
	regexp.MustCompile(`(?i)gemini\.google\.com/(app|share)`),
	regexp.MustCompile(`(?i)g\.co/gemini/share`),
	regexp.MustCompile(`(?i)jules\.google\.com/`),
	regexp.MustCompile(`(?i)app\.devin\.ai/`),
	regexp.MustCompile(`(?i)cursor\.com/agents`),
}

// Sanitize removes agent attribution from a commit message and reports what it took out.
//
// Dropping these costs nothing and burning an iteration on them costs a pass, so this isn't a finding:
// an agent adding its own footer has written the same change either way, and the operator's sign-off is
// what the commit needs.
func Sanitize(message string) (string, []string) {
	var kept, dropped []string
	for line := range strings.SplitSeq(message, "\n") {
		if matched := match(line); matched != "" {
			dropped = append(dropped, strings.TrimSpace(line))
			continue
		}
		kept = append(kept, line)
	}
	if len(dropped) == 0 {
		return message, nil
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n", dropped
}

// match returns the line when any attribution pattern claims it.
func match(line string) string {
	for _, re := range attribution {
		if re.MatchString(line) {
			return line
		}
	}
	return ""
}
