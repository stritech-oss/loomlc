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
var authHeader = regexp.MustCompile(`(?i)\b(authorization:\s*(?:bearer|basic|token)\s+)\S+`)

var (
	privateKeyBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	privateKeyEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY-----`)
)

// secretEnvName matches environment variable names that conventionally hold secrets.
var secretEnvName = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIALS?|API_?KEY|(^|_)KEY)$`)

// Redactor masks known credential formats, private key blocks, and specific secret values.
type Redactor struct {
	values []string // longest first, so a secret that contains another is masked whole
}

// New returns a Redactor that masks the built-in credential formats and each of values. Values shorter
// than eight characters are ignored.
func New(values ...string) *Redactor {
	var kept []string
	for _, v := range values {
		if len(v) >= minSecretLen && !slices.Contains(kept, v) {
			kept = append(kept, v)
		}
	}
	slices.SortFunc(kept, func(a, b string) int { return len(b) - len(a) })
	return &Redactor{values: kept}
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
	inKey := false
	for _, line := range strings.SplitAfter(s, "\n") {
		var masked string
		masked, inKey = r.line(line, inKey)
		b.WriteString(masked)
	}
	return b.String()
}

// Writer returns a writer that masks secrets before writing to w. It holds text back until a newline,
// or until 64 KiB have accumulated, so a secret split across writes is still caught. Close writes
// whatever is held back; it doesn't close w.
func (r *Redactor) Writer(w io.Writer) io.WriteCloser {
	return &writer{r: r, w: w}
}

// line masks a single line, including its trailing newline if any. inKey reports whether a private key
// block began on an earlier line and hasn't ended; the returned bool reports the same after this line.
func (r *Redactor) line(line string, inKey bool) (string, bool) {
	if inKey || privateKeyBegin.MatchString(line) {
		newline := ""
		if strings.HasSuffix(line, "\n") {
			newline = "\n"
		}
		return Mask + newline, !privateKeyEnd.MatchString(line)
	}

	for _, v := range r.values {
		line = strings.ReplaceAll(line, v, Mask)
	}
	for _, p := range tokenPatterns {
		line = p.ReplaceAllString(line, Mask)
	}
	return authHeader.ReplaceAllString(line, "${1}"+Mask), false
}

type writer struct {
	r     *Redactor
	w     io.Writer
	buf   []byte
	inKey bool
}

func (lw *writer) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		end := bytes.IndexByte(lw.buf, '\n') + 1
		if end == 0 {
			if len(lw.buf) < maxLine {
				return len(p), nil
			}
			end = len(lw.buf)
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
	masked, inKey := lw.r.line(string(chunk), lw.inKey)
	lw.inKey = inKey
	_, err := io.WriteString(lw.w, masked)
	return err
}
