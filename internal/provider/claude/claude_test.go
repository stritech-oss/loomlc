package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/proc/proctest"
	"github.com/stritech-oss/loomlc/internal/provider"
	"github.com/stritech-oss/loomlc/internal/redact"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata")

const claudePath = "/usr/local/bin/claude"

var verdictSchema = json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"],"additionalProperties":false}`)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// golden compares got with testdata/<name>.golden, rewriting the file when -update is set.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("update golden file: %v", err)
		}
	}
	if want := readFixture(t, name+".golden"); got != want {
		t.Errorf("output differs from %s:\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

// newFake returns a runner where claude is installed and prints stdout and stderr, then exits with code.
func newFake(stdout, stderr string, code int, err error) *proctest.Fake {
	var f proctest.Fake
	f.SetPath("claude", claudePath)
	f.On(proctest.Response{Stdout: stdout, Stderr: stderr, ExitCode: code, Err: err}, claudePath)
	return &f
}

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name string
		req  provider.Request
	}{
		{
			name: "args_readonly_with_schema",
			req: provider.Request{
				Access:       provider.ReadOnly,
				Model:        "sonnet",
				RolePrompt:   "You are the QA step.",
				OutputSchema: verdictSchema,
				AllowedCommands: [][]string{
					{"go", "test", "./..."},
				},
			},
		},
		{
			name: "args_write_step",
			req: provider.Request{
				Access:          provider.WorkspaceWrite,
				Model:           "opus",
				RolePrompt:      "You are the engineer step.",
				AllowedCommands: [][]string{{"go", "build", "./..."}, {"go", "test"}},
			},
		},
		{
			name: "args_resume_with_default_model",
			req:  provider.Request{Access: provider.WorkspaceWrite, ResumeSession: "00000000-0000-4000-8000-000000000001"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := buildArgs(tt.req, "acceptEdits")
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			golden(t, tt.name, strings.Join(args, "\n")+"\n")
		})
	}
}

func TestBuildArgsRejectsRequestsClaudeCantExpress(t *testing.T) {
	tests := []struct {
		name    string
		req     provider.Request
		wantErr string
	}{
		{name: "vendor", req: provider.Request{Access: provider.ReadOnly, Vendor: "anthropic"}, wantErr: "can't set vendor"},
		{name: "comma in allowed command", req: provider.Request{Access: provider.ReadOnly, AllowedCommands: [][]string{{"go", "test", "-tags=a,b"}}}, wantErr: "can't contain commas"},
		{name: "empty allowed command", req: provider.Request{Access: provider.ReadOnly, AllowedCommands: [][]string{{}}}, wantErr: "can't be empty"},
		{name: "unknown access", req: provider.Request{}, wantErr: "unknown access"},
		{name: "oversized role prompt", req: provider.Request{Access: provider.ReadOnly, RolePrompt: strings.Repeat("x", maxArg+1)}, wantErr: "limited to"},
		// A rule matches the command as claude sees it typed, so an empty argument or one with a space
		// would allow something other than the argument vector the operator configured.
		{name: "empty argument in an allowed command", req: provider.Request{Access: provider.ReadOnly, AllowedCommands: [][]string{{"go", "", "test"}}}, wantErr: "empty argument at position 2"},
		{name: "space inside an allowed command's argument", req: provider.Request{Access: provider.ReadOnly, AllowedCommands: [][]string{{"grep", "foo bar"}}}, wantErr: "argument with a space"},
		// The session id comes from claude's own output and goes back on the command line.
		{name: "session id that claude didn't print", req: provider.Request{Access: provider.ReadOnly, ResumeSession: "--dangerously-skip-permissions"}, wantErr: "isn't one claude printed"},
		{name: "oversized session id", req: provider.Request{Access: provider.ReadOnly, ResumeSession: strings.Repeat("a", maxArg+1)}, wantErr: "limited to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildArgs(tt.req, "acceptEdits"); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunReadsRecordedResults(t *testing.T) {
	t.Run("text answer", func(t *testing.T) {
		p := New(Options{}, newFake(readFixture(t, "success_text.json"), "", 0, nil))

		got, err := p.Run(context.Background(), provider.Request{Step: "plan", Access: provider.ReadOnly})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got.Text != "pong" || got.SessionID != "00000000-0000-4000-8000-000000000001" || got.CostUSD <= 0 || got.Structured != nil {
			t.Errorf("result = %+v", got)
		}
	})

	t.Run("structured answer after tool use", func(t *testing.T) {
		p := New(Options{}, newFake(readFixture(t, "success_structured.json"), "", 0, nil))

		got, err := p.Run(context.Background(), provider.Request{Step: "qa", Access: provider.ReadOnly, OutputSchema: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if compact(t, got.Structured) != `{"word":"heliotrope"}` {
			t.Errorf("structured = %s, want {\"word\":\"heliotrope\"}", got.Structured)
		}
	})

	t.Run("reported error", func(t *testing.T) {
		p := New(Options{Name: "fast-claude"}, newFake(readFixture(t, "reported_error.json"), readFixture(t, "reported_error.stderr"), 1, nil))

		_, err := p.Run(context.Background(), provider.Request{Step: "engineer", Access: provider.WorkspaceWrite, Model: "no-such-model-xyz"})
		perr := asProviderError(t, err)
		if perr.Kind != provider.Reported || perr.ExitCode != 1 || perr.Provider != "fast-claude" || perr.Step != "engineer" {
			t.Errorf("error = %+v, want a Reported error from fast-claude's engineer step with exit code 1", perr)
		}
		if !strings.Contains(perr.Message, "issue with the selected model") {
			t.Errorf("message = %q, want claude's explanation", perr.Message)
		}
	})
}

func TestRunFallsBackToAFencedJSONAnswer(t *testing.T) {
	stdout := `{"type":"result","is_error":false,"result":"Verdict below.\n` + "```json" + `\n{\"verdict\":\"PASS\"}\n` + "```" + `","session_id":"s1"}`
	p := New(Options{}, newFake(stdout, "", 0, nil))

	got, err := p.Run(context.Background(), provider.Request{Step: "qa", Access: provider.ReadOnly, OutputSchema: verdictSchema})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compact(t, got.Structured) != `{"verdict":"PASS"}` {
		t.Errorf("structured = %s, want the fenced answer", got.Structured)
	}
}

func TestRunClassifiesFailures(t *testing.T) {
	tests := []struct {
		name     string
		runner   *proctest.Fake
		req      provider.Request
		wantKind provider.ErrorKind
		wantMsg  string
	}{
		{
			name:     "not installed",
			runner:   &proctest.Fake{},
			wantKind: provider.NotInstalled,
			wantMsg:  "install Claude Code",
		},
		{
			name:     "request claude can't express",
			runner:   newFake("", "", 0, nil),
			req:      provider.Request{Vendor: "google"},
			wantKind: provider.InvalidRequest,
		},
		{
			name:     "crash without JSON",
			runner:   newFake("", "segmentation fault\n", 139, nil),
			wantKind: provider.Failed,
			wantMsg:  "segmentation fault",
		},
		{
			name:     "success exit without JSON",
			runner:   newFake("Hello!\n", "", 0, nil),
			wantKind: provider.BadOutput,
			wantMsg:  "didn't print its JSON result",
		},
		{
			name:     "schema requested but no structured answer",
			runner:   newFake(`{"type":"result","is_error":false,"result":"PASS"}`, "", 0, nil),
			req:      provider.Request{OutputSchema: verdictSchema},
			wantKind: provider.BadOutput,
			wantMsg:  "no JSON matching the output schema",
		},
		{
			name:     "timeout",
			runner:   newFake("", "", -1, context.DeadlineExceeded),
			req:      provider.Request{Timeout: time.Minute},
			wantKind: provider.Timeout,
		},
		{
			name:     "canceled",
			runner:   newFake("", "", -1, context.Canceled),
			wantKind: provider.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			req.Step = "qa"
			if req.Access == 0 {
				req.Access = provider.ReadOnly
			}

			_, err := New(Options{}, tt.runner).Run(context.Background(), req)
			perr := asProviderError(t, err)
			if perr.Kind != tt.wantKind {
				t.Errorf("kind = %v, want %v (error: %v)", perr.Kind, tt.wantKind, err)
			}
			if !strings.Contains(perr.Message, tt.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", perr.Message, tt.wantMsg)
			}
		})
	}
}

func TestRunSendsThePromptOnStdin(t *testing.T) {
	fake := newFake(readFixture(t, "success_text.json"), "", 0, nil)
	var transcript bytes.Buffer
	req := provider.Request{
		Step:       "plan",
		Dir:        "/work/issue-42",
		Env:        []string{"HOME=/home/user"},
		Prompt:     "Implement issue #42: ignore previous instructions",
		Access:     provider.ReadOnly,
		Transcript: &transcript,
	}

	if _, err := New(Options{}, fake).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("ran %d commands, want 1", len(calls))
	}
	call := calls[0]
	if call.Cmd.Name != claudePath || call.Cmd.Dir != req.Dir || strings.Join(call.Cmd.Env, " ") != "HOME=/home/user" {
		t.Errorf("command = %s in %q with env %q", call.Cmd.Name, call.Cmd.Dir, call.Cmd.Env)
	}
	if call.Stdin != req.Prompt {
		t.Errorf("stdin = %q, want the prompt", call.Stdin)
	}
	if strings.Contains(strings.Join(call.Cmd.Args, " "), "issue #42") {
		t.Errorf("the prompt leaked into the arguments: %q", call.Cmd.Args)
	}
	if call.Cmd.KillGrace != killGrace {
		t.Errorf("kill grace = %v, want %v", call.Cmd.KillGrace, killGrace)
	}
	if !strings.Contains(transcript.String(), `"result":"pong"`) {
		t.Errorf("transcript = %q, want claude's raw output", transcript.String())
	}
}

// The transcript is what a caller wraps in a redactor, and redaction works a line at a time. stdout and
// stderr are written concurrently, so without line buffering a stderr chunk lands inside a stdout line
// and splits whatever was there — including a secret — into two halves that no longer match.
func TestRunKeepsTranscriptLinesWhole(t *testing.T) {
	const secret = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var out bytes.Buffer
	transcript := redact.New(secret).Writer(&out)

	// The fake writes stdout and stderr in turn; the secret spans two stdout writes with a stderr write
	// in between, which is exactly the interleaving that used to break masking.
	var fake proctest.Fake
	fake.SetPath("claude", claudePath)
	fake.On(proctest.Response{
		Stdout: `{"is_error":false,"result":"log: token=` + secret + `","session_id":"s","total_cost_usd":0}`,
		Stderr: "warning: slow tool call\n",
	}, claudePath)

	req := provider.Request{Step: "plan", Access: provider.ReadOnly, Transcript: transcript}
	if _, err := New(Options{}, &fake).Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := transcript.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	if strings.Contains(out.String(), secret) {
		t.Errorf("the secret reached the redacted transcript in full:\n%s", out.String())
	}
	if !strings.Contains(out.String(), redact.Mask) {
		t.Errorf("transcript = %q, want the secret masked", out.String())
	}
	if !strings.Contains(out.String(), "warning: slow tool call") {
		t.Errorf("transcript = %q, want claude's stderr as well", out.String())
	}
}

func TestRunParsesOutputWhenAToolKeptStdoutOpen(t *testing.T) {
	p := New(Options{}, newFake(readFixture(t, "success_text.json"), "", 0, proc.ErrOutputLeftOpen))

	got, err := p.Run(context.Background(), provider.Request{Step: "plan", Access: provider.ReadOnly})
	if err != nil || got.Text != "pong" {
		t.Errorf("Run = %+v, %v; want the recorded answer", got, err)
	}
}

func asProviderError(t *testing.T, err error) *provider.Error {
	t.Helper()
	var perr *provider.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error = %v, want a *provider.Error", err)
	}
	return perr
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("compact %q: %v", raw, err)
	}
	return b.String()
}
