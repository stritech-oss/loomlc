// Package provider defines the contract between loomlc's engine and the provider adapters that run steps
// with agent CLIs.
package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxMessage bounds Error.Message, so a CLI that prints megabytes doesn't flood logs and comments.
const MaxMessage = 4 << 10

// Access is what a step may do in its workspace.
type Access int

const (
	// ReadOnly steps read the workspace and must not change it.
	ReadOnly Access = iota + 1
	// WorkspaceWrite steps may edit files in the workspace.
	WorkspaceWrite
)

// Capabilities describes what an adapter's CLI supports, so the engine can adapt to it.
type Capabilities struct {
	// StructuredOutput means the CLI enforces a JSON Schema on the final answer.
	StructuredOutput bool
	// Resume means a session can be continued by its id.
	Resume bool
	// PermissionModel means the CLI enforces its own tool permissions or sandbox.
	PermissionModel bool
	// RequiresSandbox means the CLI has no permission model, so it may only run in an isolated executor
	// unless the operator explicitly allows otherwise.
	RequiresSandbox bool
	// SystemPrompt means role instructions go in a system prompt instead of the task prompt.
	SystemPrompt bool
	// CostReport means the CLI reports what a run cost.
	CostReport bool
}

// Request is one step for a provider to run.
type Request struct {
	// Step names the step, for errors.
	Step string
	// Dir is the workspace the agent works in.
	Dir string
	// RolePrompt is the step's trusted instructions.
	RolePrompt string
	// Prompt is the task context. It includes untrusted text, so adapters send it on stdin, never in
	// command-line arguments.
	Prompt string
	// Model is the model to use; empty means the CLI's default.
	Model string
	// Vendor is the model vendor, for aggregators such as pi.
	Vendor string
	Access Access
	// AllowedCommands are argument-vector prefixes the agent may run, for CLIs with a permission model.
	AllowedCommands [][]string
	// OutputSchema is a JSON Schema the final answer must match. Nil means free text.
	OutputSchema json.RawMessage
	// ResumeSession continues an earlier session instead of starting a new one.
	ResumeSession string
	// Timeout bounds the run. Zero means no limit beyond the caller's context.
	Timeout time.Duration
	// Env is the CLI's complete environment. Nothing else is inherited.
	Env []string
	// Transcript, if set, receives the CLI's raw output. The caller must redact it before storing it.
	Transcript io.Writer
}

// Result is a successful run.
type Result struct {
	// Text is the agent's final answer.
	Text string
	// Structured is the final answer as JSON, when Request.OutputSchema was set.
	Structured json.RawMessage
	// SessionID identifies the session, for Request.ResumeSession.
	SessionID string
	// CostUSD is what the run cost, when the CLI reports it.
	CostUSD float64
}

// ErrorKind classifies a failed run.
type ErrorKind int

const (
	// NotInstalled means the CLI's executable wasn't found.
	NotInstalled ErrorKind = iota + 1
	// InvalidRequest means the request can't be expressed for this CLI.
	InvalidRequest
	// Timeout means the run took longer than Request.Timeout.
	Timeout
	// Canceled means the caller canceled the run.
	Canceled
	// Failed means the CLI exited unsuccessfully without an explanation loomlc could read.
	Failed
	// Reported means the CLI ran and reported an error, such as an unknown model or failed authentication.
	Reported
	// BadOutput means the CLI finished but its output couldn't be read or lacked the required answer.
	BadOutput
)

func (k ErrorKind) String() string {
	switch k {
	case NotInstalled:
		return "not installed"
	case InvalidRequest:
		return "invalid request"
	case Timeout:
		return "timed out"
	case Canceled:
		return "canceled"
	case Failed:
		return "failed"
	case Reported:
		return "reported an error"
	case BadOutput:
		return "unreadable output"
	default:
		return fmt.Sprintf("error kind %d", int(k))
	}
}

// Error is a failed run.
type Error struct {
	Provider string
	Step     string
	Kind     ErrorKind
	// ExitCode is the CLI's exit status, when it ran.
	ExitCode int
	// Message explains the failure. It can contain CLI output, so redact it before storing or posting it.
	Message string
	Err     error
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("provider %s, step %s: %s", e.Provider, e.Step, e.Kind)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Tail returns s without surrounding whitespace, cut to its last MaxMessage bytes. The cut lands on a
// character boundary: a message with a character sliced in half is invalid UTF-8, and JSON encoding
// would replace the pieces before an operator ever read it.
func Tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxMessage {
		return s
	}
	cut := len(s) - MaxMessage
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "…" + s[cut:]
}

var fencedJSON = regexp.MustCompile("(?is)```json[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")

// FencedJSON returns the last fenced json code block in text that holds valid JSON. Adapters use it when
// a CLI can't enforce a schema, or doesn't return the structured answer it enforced.
func FencedJSON(text string) (json.RawMessage, bool) {
	blocks := fencedJSON.FindAllStringSubmatch(text, -1)
	for i := len(blocks) - 1; i >= 0; i-- {
		body := strings.TrimSpace(blocks[i][1])
		if json.Valid([]byte(body)) {
			return json.RawMessage(body), true
		}
	}
	return nil, false
}
