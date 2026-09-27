package run

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/stritech-oss/loomlc/internal/redact"
)

// Match is one place a scan found something that mustn't be published.
type Match struct {
	Path string
	Line int
	// Rule says what matched, never the text that matched it.
	Rule string
}

// hunk marks where a patch says the following lines land in the new file.
var hunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// Secrets finds credentials in a diff by asking the redactor: a line that comes back masked is a line
// that mustn't be published. Reusing the masking means the scan knows exactly what loomlc would have
// hidden in a log, rather than a second, differently-wrong list of patterns.
//
// It is a floor, not a secret scanner: no entropy analysis, and only the formats redact knows. An
// operator wanting more adds gitleaks or the like as a publish gate.
type Secrets struct {
	// formats masks the credential shapes loomlc knows.
	formats *redact.Redactor
	// values are this run's own secrets, the case a generic scanner can't see. Held as strings because a
	// Redactor built from them also carries the built-in patterns, which would make every match look
	// like one of these.
	values []string
	// skip are paths whose credential-shaped content is deliberate, such as test fixtures.
	skip []string
}

// NewSecrets returns a scanner for the credential formats loomlc knows and the values in env, skipping
// the given paths.
func NewSecrets(env, skip []string) *Secrets {
	var values []string
	for _, v := range redact.SecretValues(env) {
		// Short values appear everywhere innocently; redact ignores them for the same reason.
		if len(v) >= minSecretLen {
			values = append(values, v)
		}
	}
	return &Secrets{formats: redact.New(), values: values, skip: skip}
}

// minSecretLen mirrors the length below which internal/redact doesn't treat a value as a secret.
const minSecretLen = 8

// Scan reports the added lines that carry a credential. Deleted lines are ignored: a secret being removed
// is the good case.
func (s *Secrets) Scan(diff string) []Match {
	var matches []Match
	path, line := "", 0
	for raw := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "+++ "):
			path, line = newPath(raw), 0
			continue
		case hunk.MatchString(raw):
			line = hunkStart(raw) - 1
			continue
		case strings.HasPrefix(raw, "-"):
			continue
		case !strings.HasPrefix(raw, "+"):
			line++ // context
			continue
		}

		line++
		if path == "" || s.skipped(path) {
			continue
		}
		added := strings.TrimPrefix(raw, "+")
		if rule := s.rule(added); rule != "" {
			matches = append(matches, Match{Path: path, Line: line, Rule: rule})
		}
	}
	return matches
}

// rule says what a line matched, without repeating what it said.
func (s *Secrets) rule(line string) string {
	for _, v := range s.values {
		if strings.Contains(line, v) {
			return "a secret from this run's own environment"
		}
	}
	if s.formats.String(line) != line {
		return "something shaped like a credential"
	}
	return ""
}

func (s *Secrets) skipped(path string) bool {
	for _, p := range s.skip {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// newPath reads the new file's path from a "+++ b/path" line. /dev/null means the file was deleted.
func newPath(header string) string {
	path := strings.TrimPrefix(header, "+++ ")
	if tab := strings.IndexByte(path, '\t'); tab >= 0 {
		path = path[:tab]
	}
	if path == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(path, "b/")
}

func hunkStart(header string) int {
	match := hunk.FindStringSubmatch(header)
	if match == nil {
		return 1
	}
	start, err := strconv.Atoi(match[1])
	if err != nil {
		return 1
	}
	return start
}
