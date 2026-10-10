// Package runlog records what a run did, under .loomlc/runs/<id>.
//
// Everything written here is redacted first: a prompt holds the task's own words and a step's answer
// holds whatever an agent read, and both land in a file an operator will open.
package runlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/stritech-oss/loomlc/internal/redact"
)

// idPattern keeps a run id to something that is safe as a directory name.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Run is the record of one run.
type Run struct {
	ID        string    `json:"id"`
	Lifecycle string    `json:"lifecycle"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Task      Task      `json:"task"`
	Branch    string    `json:"branch"`
	Base      string    `json:"base"`
	// Backup is the ref holding commits an earlier attempt left on the branch, when there were any. It
	// is the only pointer to that work once the branch has been reset.
	Backup string `json:"backup,omitempty"`
	// Outcome is how the lifecycle ended: proposed, blocked, or exhausted.
	Outcome string   `json:"outcome"`
	Passes  int      `json:"passes"`
	Commits []Commit `json:"commits,omitempty"`
	Gates   []Gate   `json:"gates,omitempty"`
	Output  *Output  `json:"output,omitempty"`
	// Blockers say why a run proposed nothing; Refusals why publishing was refused.
	Blockers []string `json:"blockers,omitempty"`
	Refusals []string `json:"refusals,omitempty"`
	// Error is set when the run failed rather than ended.
	Error string `json:"error,omitempty"`
}

// Task is the task a run answered.
type Task struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// Commit is one commit the engine made.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// Gate is one check and whether it passed.
type Gate struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

// Output is where the work was published.
type Output struct {
	Ref string `json:"ref"`
	URL string `json:"url"`
}

// Writer records a run under root.
type Writer struct {
	dir     string
	redact  *redact.Redactor
	written int
}

// New returns a Writer for run id under root, creating its directory. root is usually
// .loomlc/runs in the operator's checkout.
func New(root, id string, r *redact.Redactor) (*Writer, error) {
	if !idPattern.MatchString(id) {
		return nil, fmt.Errorf("runlog: run id %q can't be a directory name", id)
	}
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("runlog: %w", err)
	}
	return &Writer{dir: dir, redact: r}, nil
}

// Dir is where the run's files are, for telling an operator where to look.
func (w *Writer) Dir() string { return w.dir }

// Save writes the run's record, replacing what was there. It is written whole each time, so a run
// interrupted part way still leaves the last thing loomlc knew.
func (w *Writer) Save(r Run) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("runlog: %w", err)
	}
	return w.write("run.json", string(body)+"\n")
}

// Step records what a step was asked and what it answered. Files are numbered in the order they were
// written, so reading the directory reads the run.
func (w *Writer) Step(name, prompt, answer string) error {
	w.written++
	n := w.written
	if err := w.write(fmt.Sprintf("%02d-%s-prompt.md", n, name), prompt); err != nil {
		return err
	}
	return w.write(fmt.Sprintf("%02d-%s-answer.json", n, name), answer)
}

func (w *Writer) write(name, content string) error {
	if w.redact != nil {
		content = w.redact.String(content)
	}
	path := filepath.Join(w.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("runlog: %w", err)
	}
	return nil
}
