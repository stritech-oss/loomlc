// Package claude runs steps with Anthropic's Claude Code CLI in headless mode (claude -p).
//
// The flags and output fields it relies on were checked against Claude Code 2.1.269; see
// docs/providers.md and testdata/README.md.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/provider"
)

const (
	// defaultCmd is the executable run when Options.Cmd is empty.
	defaultCmd = "claude"
	// defaultWriteMode is the permission mode for steps that edit files when Options.PermissionMode is empty.
	defaultWriteMode = "acceptEdits"
	// readOnlyMode denies every action that would need approval, while still allowing file reads.
	readOnlyMode = "dontAsk"
	// killGrace gives claude time to stop the tools it started after SIGTERM.
	killGrace = 15 * time.Second
	// maxArg keeps each argument below Linux's 128 KiB per-argument limit.
	maxArg = 100 << 10
)

// Capabilities returns what the claude adapter supports.
func Capabilities() provider.Capabilities {
	return provider.Capabilities{
		StructuredOutput: true,
		Resume:           true,
		PermissionModel:  true,
		SystemPrompt:     true,
		CostReport:       true,
	}
}

// Options configure a Provider.
type Options struct {
	// Name is the provider's name in configuration, used in errors. Empty means "claude".
	Name string
	// Cmd is the claude executable, as a name or a path. Empty means "claude".
	Cmd string
	// PermissionMode is the claude permission mode for steps that edit files. Empty means acceptEdits.
	PermissionMode string
}

// Provider runs steps with the claude CLI.
type Provider struct {
	name      string
	cmd       string
	writeMode string
	runner    proc.Runner
}

// New returns a Provider that starts claude through runner.
func New(opts Options, runner proc.Runner) *Provider {
	p := &Provider{name: opts.Name, cmd: opts.Cmd, writeMode: opts.PermissionMode, runner: runner}
	if p.name == "" {
		p.name = defaultCmd
	}
	if p.cmd == "" {
		p.cmd = defaultCmd
	}
	if p.writeMode == "" {
		p.writeMode = defaultWriteMode
	}
	return p
}

// Capabilities returns what the claude adapter supports.
func (p *Provider) Capabilities() provider.Capabilities { return Capabilities() }

// Run runs one step with claude and returns its final answer.
func (p *Provider) Run(ctx context.Context, req provider.Request) (provider.Result, error) {
	fail := func(kind provider.ErrorKind, exitCode int, message string, err error) error {
		return &provider.Error{Provider: p.name, Step: req.Step, Kind: kind, ExitCode: exitCode, Message: provider.Tail(message), Err: err}
	}

	args, err := buildArgs(req, p.writeMode)
	if err != nil {
		return provider.Result{}, fail(provider.InvalidRequest, 0, err.Error(), nil)
	}
	path, err := p.runner.LookPath(p.cmd)
	if err != nil {
		return provider.Result{}, fail(provider.NotInstalled, 0, fmt.Sprintf("install Claude Code, or set providers.%s.cmd to its path", p.name), err)
	}

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	var stdout, stderr bytes.Buffer
	outW, errW := io.Writer(&stdout), io.Writer(&stderr)
	if req.Transcript != nil {
		transcript := &lockedWriter{w: req.Transcript}
		outW, errW = io.MultiWriter(&stdout, transcript), io.MultiWriter(&stderr, transcript)
	}

	res, runErr := p.runner.Run(ctx, proc.Cmd{
		Name:      path,
		Args:      args,
		Dir:       req.Dir,
		Env:       req.Env,
		Stdin:     strings.NewReader(req.Prompt),
		Stdout:    outW,
		Stderr:    errW,
		KillGrace: killGrace,
	})
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		return provider.Result{}, fail(provider.Timeout, res.ExitCode, "", runErr)
	case errors.Is(runErr, context.Canceled):
		return provider.Result{}, fail(provider.Canceled, res.ExitCode, "", runErr)
	case runErr != nil && !errors.Is(runErr, proc.ErrOutputLeftOpen):
		// With ErrOutputLeftOpen, claude finished and a tool it started kept stdout open; the result it
		// printed first is still readable, so it's parsed below.
		return provider.Result{}, fail(provider.Failed, res.ExitCode, stderr.String(), runErr)
	}
	return parse(req, res.ExitCode, stdout.Bytes(), stderr.String(), fail)
}

// output is the part of claude's --output-format json result that loomlc reads.
type output struct {
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

type failFunc func(kind provider.ErrorKind, exitCode int, message string, err error) error

func parse(req provider.Request, exitCode int, stdout []byte, stderr string, fail failFunc) (provider.Result, error) {
	var out output
	if err := json.Unmarshal(stdout, &out); err != nil {
		if exitCode != 0 {
			return provider.Result{}, fail(provider.Failed, exitCode, firstNonEmpty(stderr, string(stdout)), nil)
		}
		return provider.Result{}, fail(provider.BadOutput, exitCode, "claude didn't print its JSON result: "+string(stdout), err)
	}
	// A failed run can still report subtype "success"; is_error and the exit code are what count.
	if out.IsError || exitCode != 0 {
		return provider.Result{}, fail(provider.Reported, exitCode, firstNonEmpty(out.Result, stderr), nil)
	}

	result := provider.Result{Text: out.Result, SessionID: out.SessionID, CostUSD: out.TotalCostUSD}
	if req.OutputSchema != nil {
		structured, ok := structuredAnswer(out)
		if !ok {
			return provider.Result{}, fail(provider.BadOutput, exitCode, "the answer has no JSON matching the output schema: "+out.Result, nil)
		}
		result.Structured = structured
	}
	return result, nil
}

// structuredAnswer returns the schema-checked answer, or a fenced json block from the text answer when
// claude didn't return one.
func structuredAnswer(out output) (json.RawMessage, bool) {
	if len(out.StructuredOutput) > 0 && !bytes.Equal(out.StructuredOutput, []byte("null")) {
		return out.StructuredOutput, true
	}
	return provider.FencedJSON(out.Result)
}

// deniedToGit returns rules for the commands loomlc runs itself: agents never commit, push, rewrite
// history, switch branches, or call GitHub.
func deniedToGit() []string {
	return []string{
		"Bash(git commit *)",
		"Bash(git push *)",
		"Bash(git reset *)",
		"Bash(git rebase *)",
		"Bash(git checkout *)",
		"Bash(git switch *)",
		"Bash(gh *)",
	}
}

// buildArgs returns claude's arguments for req. The prompt itself goes on stdin, never here.
func buildArgs(req provider.Request, writeMode string) ([]string, error) {
	if req.Vendor != "" {
		return nil, fmt.Errorf("claude chooses its own vendor, so a step can't set vendor %q", req.Vendor)
	}
	for name, value := range map[string]string{"role prompt": req.RolePrompt, "output schema": string(req.OutputSchema)} {
		if len(value) > maxArg {
			return nil, fmt.Errorf("the %s is %d bytes; claude takes it as an argument, which is limited to %d", name, len(value), maxArg)
		}
	}

	allowed := []string{"Bash(git log *)", "Bash(git diff *)", "Bash(git show *)", "Bash(git status *)"}
	for _, argv := range req.AllowedCommands {
		rule, err := bashRule(argv)
		if err != nil {
			return nil, err
		}
		allowed = append(allowed, rule)
	}

	mode, denied := writeMode, deniedToGit()
	switch req.Access {
	case provider.WorkspaceWrite:
	case provider.ReadOnly:
		mode = readOnlyMode
		denied = append([]string{"Edit", "Write", "NotebookEdit"}, denied...)
	default:
		return nil, fmt.Errorf("unknown access %d", req.Access)
	}

	// Tool lists go first, each as one comma-separated argument, so claude's variadic flag parsing can't
	// swallow the flags after them.
	args := []string{
		"-p",
		"--output-format", "json",
		"--allowedTools", strings.Join(allowed, ","),
		"--disallowedTools", strings.Join(denied, ","),
		"--permission-mode", mode,
		"--permission-prompts", "none",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.RolePrompt != "" {
		args = append(args, "--append-system-prompt", req.RolePrompt)
	}
	if len(req.OutputSchema) > 0 {
		args = append(args, "--json-schema", string(req.OutputSchema))
	}
	if req.ResumeSession != "" {
		args = append(args, "--resume", req.ResumeSession)
	}
	return args, nil
}

// bashRule turns an argument-vector prefix into a claude permission rule that allows the command with any
// further arguments.
func bashRule(argv []string) (string, error) {
	joined := strings.Join(argv, " ")
	switch {
	case len(argv) == 0 || argv[0] == "":
		return "", errors.New("an allowed command can't be empty")
	case strings.ContainsAny(joined, ",()"):
		return "", fmt.Errorf("allowed command %q can't contain commas or parentheses, because claude reads its tool rules as a comma-separated list", joined)
	default:
		return "Bash(" + joined + " *)", nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// lockedWriter serializes writes, because stdout and stderr are copied into the transcript concurrently.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
