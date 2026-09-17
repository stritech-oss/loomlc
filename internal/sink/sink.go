// Package sink describes where loomlc publishes a run's result, such as a pull request.
package sink

import (
	"fmt"
	"time"
)

const (
	// Marker starts the HTML comment loomlc puts in text it posts, so it recognizes its own comments later
	// and doesn't treat them as feedback.
	Marker = "<!-- loomlc:"
	// ReplyMarker marks loomlc's reply to review feedback, which is where "since the last reply" starts.
	ReplyMarker = Marker + "feedback-reply"
)

// Ref identifies an output within its sink.
type Ref struct {
	// Sink is the configured sink's name.
	Sink string
	// ID identifies the output inside that sink: "44" for a GitHub pull request, and whatever another
	// sink calls its own results. It follows the same rules as a task's id (see internal/task).
	ID string
}

func (r Ref) String() string { return fmt.Sprintf("%s#%s", r.Sink, r.ID) }

// Change is a result to publish. The branch it names is already pushed.
type Change struct {
	Branch string
	Base   string
	Title  string
	// Body is the description text.
	Body     string
	Labels   []string
	Reviewer string
	Draft    bool
}

// Output is a published result, such as an open pull request.
type Output struct {
	Ref    Ref
	URL    string
	Branch string
	Base   string
	Open   bool
	// CrossRepo means the branch lives in a fork, which loomlc can't push to.
	CrossRepo bool
	Labels    []string
	// Warnings record what didn't work but didn't stop publishing, such as a review that couldn't be
	// requested.
	Warnings []string
}

// FeedbackItem is one piece of human review feedback on an output.
type FeedbackItem struct {
	ID string
	// Kind is "review", "comment", or "inline".
	Kind   string
	Author string
	// Path and Line locate an inline comment in the diff.
	Path string
	Line int
	// Body is the reviewer's text. It's untrusted, like a task's description.
	Body      string
	CreatedAt time.Time
}
