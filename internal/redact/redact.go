// Package redact masks secrets in text before loomlc logs it, stores it, or sends it anywhere.
package redact

import (
	"bytes"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Mask replaces each secret.
const Mask = "[REDACTED]"

// minSecretLen is the length below which a value isn't treated as a secret, so short, common strings
// from the environment aren't masked everywhere they appear.
const minSecretLen = 8

// maxLine bounds how much a Writer holds back while waiting for a newline.
const maxLine = 64 << 10

// minHoldBack is how much of a newline-less line a Writer keeps for the next write, so a secret lying
// across the flush boundary is still whole when it's masked. It's a floor: a Redactor holds back at
// least its longest secret.
const minHoldBack = 256

// maxKeyLines bounds a private key block. Without a bound, a "BEGIN PRIVATE KEY" line with no matching
// END — which anyone who can write a task description or a review comment can produce — would mask the
// rest of the run's output and hide everything the operator needs to see.
const maxKeyLines = 200

// tokenPatterns match credential formats from common providers. They are masked wherever they appear.
var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`),   // GitHub personal, OAuth, user, server, and refresh tokens
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), // GitHub fine-grained tokens
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),     // GitLab personal access tokens
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),        // Anthropic (sk-ant-…) and OpenAI (sk-…, sk-proj-…) API keys
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),  // AWS access key IDs
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),        // Google API keys
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), // Slack tokens
}

// authHeader matches an Authorization header's credential. The scheme is kept, so logs still show a
// credential was sent.
var authHeader = regexp.MustCompile(`(?i)\b(authorization:\s*(?:bearer|basic|token|dpop|digest)\s+)\S+`)

// secretHeader matches headers whose whole value is a credential, so nothing of it is kept.
var secretHeader = regexp.MustCompile(`(?i)\b(x-api-key:\s*|private-token:\s*|x-auth-token:\s*|x-amz-security-token:\s*)\S+`)

var (
	privateKeyBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	privateKeyEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// secretEnvName matches environment variable names that conventionally hold secrets.
var secretEnvName = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|CREDENTIALS?|API_?KEY|(^|_)(KEY|PAT|PWD))$`)

// Redactor masks known credential formats, private key blocks, and specific secret values.
type Redactor struct {
	values   []string // longest first, so a secret that contains another is masked whole
	holdBack int      // how much a Writer keeps back when flushing a line with no newline
}

// New returns a Redactor that masks the built-in credential formats and each of values. Values shorter
// than eight characters are ignored. A value spanning several lines is masked line by line, since
// masking works a line at a time and a multi-line value would otherwise never match at all.
func New(values ...string) *Redactor {
	var kept []string
	add := func(v string) {
		if len(v) >= minSecretLen && !slices.Contains(kept, v) {
			kept = append(kept, v)
		}
	}
	for _, v := range values {
		if !strings.Contains(v, "\n") {
			add(v)
			continue
		}
		for line := range strings.SplitSeq(v, "\n") {
			add(strings.TrimRight(line, "\r"))
		}
	}
	slices.SortFunc(kept, func(a, b string) int { return len(b) - len(a) })

	hold := minHoldBack
	if len(kept) > 0 {
		hold = max(hold, len(kept[0]))
	}
	return &Redactor{values: kept, holdBack: hold}
}

// SecretValues returns the values of the entries in env (KEY=value) whose names conventionally hold
// secrets, such as GH_TOKEN or ANTHROPIC_API_KEY, for passing to New.
func SecretValues(env []string) []string {
	var values []string
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && value != "" && secretEnvName.MatchString(name) {
			values = append(values, value)
		}
	}
	return values
}

// String returns s with every secret masked.
func (r *Redactor) String(s string) string {
	var b strings.Builder
	var key keyState
	for line := range strings.SplitAfterSeq(s, "\n") {
		if line == "" {
			continue // SplitAfter's empty tail after a final newline
		}
		var masked string
		masked, key = r.line(line, key)
		b.WriteString(masked)
	}
	return b.String()
}

// keyState tracks a private key block across lines, and how many lines it has masked, so an unterminated
// block gives up instead of masking everything that follows.
type keyState struct {
	inKey bool
	lines int
}

// Writer returns a writer that masks secrets before writing to w. It holds text back until a newline,
// or until 64 KiB have accumulated, so a secret split across writes is still caught. Close writes
// whatever is held back; it doesn't close w.
func (r *Redactor) Writer(w io.Writer) io.WriteCloser {
	return &writer{r: r, w: w}
}

// line masks a single line, including its trailing newline if any. key carries a private key block from
// earlier lines; the returned state carries it on.
func (r *Redactor) line(line string, key keyState) (string, keyState) {
	if key.inKey || privateKeyBegin.MatchString(line) {
		newline := ""
		if strings.HasSuffix(line, "\n") {
			newline = "\n"
		}
		key.lines++
		// A block that never ends stops being believable: give up rather than mask the rest of the run.
		key.inKey = !privateKeyEnd.MatchString(line) && key.lines < maxKeyLines
		return Mask + newline, key
	}

	for _, v := range r.values {
		line = strings.ReplaceAll(line, v, Mask)
	}
	for _, p := range tokenPatterns {
		line = p.ReplaceAllString(line, Mask)
	}
	line = authHeader.ReplaceAllString(line, "${1}"+Mask)
	return secretHeader.ReplaceAllString(line, "${1}"+Mask), keyState{}
}

type writer struct {
	r   *Redactor
	w   io.Writer
	buf []byte
	key keyState
}

func (lw *writer) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		end := bytes.IndexByte(lw.buf, '\n') + 1
		if end == 0 {
			if len(lw.buf) < maxLine {
				return len(p), nil
			}
			// A line this long has to be written out before it's whole. Keep back the tail, so a
			// secret lying across the cut is masked when the rest of it arrives instead of being
			// written out in two unrecognizable halves.
			end = len(lw.buf) - lw.r.holdBack
			if end <= 0 {
				return len(p), nil
			}
		}
		if err := lw.emit(lw.buf[:end]); err != nil {
			return 0, err
		}
		lw.buf = lw.buf[end:]
	}
}

// Close writes any text still held back.
func (lw *writer) Close() error {
	if len(lw.buf) == 0 {
		return nil
	}
	err := lw.emit(lw.buf)
	lw.buf = nil
	return err
}

func (lw *writer) emit(chunk []byte) error {
	masked, key := lw.r.line(string(chunk), lw.key)
	lw.key = key
	_, err := io.WriteString(lw.w, masked)
	return err
}
