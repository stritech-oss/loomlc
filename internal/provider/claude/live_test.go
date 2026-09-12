//go:build live

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/proc"
	"github.com/stritech-oss/loomlc/internal/provider"
)

// TestLiveClaude runs the adapter against the installed claude CLI, using the claude subscription or API
// key of whoever runs it. The live build tag keeps it out of `task check` and CI:
//
//	go test -tags live ./internal/provider/claude -run TestLive -v
func TestLiveClaude(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("The magic word is heliotrope.\n"), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
	schema := json.RawMessage(`{"type":"object","properties":{"word":{"type":"string"}},"required":["word"],"additionalProperties":false}`)
	p := New(Options{}, proc.Exec{})
	ctx := context.Background()
	base := provider.Request{Dir: dir, Env: liveEnv(), Model: "haiku", Timeout: 3 * time.Minute}

	read := base
	read.Step = "read"
	read.Access = provider.ReadOnly
	read.RolePrompt = "You are a careful reader. Answer only through the required schema."
	read.Prompt = "Read note.txt and report the magic word."
	read.OutputSchema = schema
	first, err := p.Run(ctx, read)
	if err != nil {
		t.Fatalf("read-only step: %v", err)
	}
	if word := decodeWord(t, first.Structured); word != "heliotrope" || first.SessionID == "" {
		t.Fatalf("read-only step answered %q with session %q", word, first.SessionID)
	}

	resume := read
	resume.Step = "resume"
	resume.Prompt = "What was the magic word you reported?"
	resume.ResumeSession = first.SessionID
	second, err := p.Run(ctx, resume)
	if err != nil {
		t.Fatalf("resumed step: %v", err)
	}
	if word := decodeWord(t, second.Structured); word != "heliotrope" || second.SessionID != first.SessionID {
		t.Fatalf("resumed step answered %q in session %q, want heliotrope in %q", word, second.SessionID, first.SessionID)
	}

	write := base
	write.Step = "write"
	write.Access = provider.WorkspaceWrite
	write.Prompt = "Create a file named hello.txt whose entire content is the word hi. Then reply done."
	if _, err := p.Run(ctx, write); err != nil {
		t.Fatalf("write step: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil || strings.TrimSpace(string(content)) != "hi" {
		t.Fatalf("write step left hello.txt = %q, %v; want hi", content, err)
	}
}

// liveEnv is this process's environment without the variables that mark a process as running inside
// Claude Code, so claude behaves as it would for an unattended loomlc run.
func liveEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDECODE=") && !strings.HasPrefix(kv, "CLAUDE_CODE_ENTRYPOINT=") {
			env = append(env, kv)
		}
	}
	return env
}

func decodeWord(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var answer struct {
		Word string `json:"word"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("decode structured answer %q: %v", raw, err)
	}
	return answer.Word
}
