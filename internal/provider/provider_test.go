package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFencedJSONReturnsTheLastValidBlock(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		want   string
		wantOK bool
	}{
		{
			name:   "single block",
			text:   "Here is the verdict:\n```json\n{\"verdict\": \"PASS\"}\n```\n",
			want:   `{"verdict": "PASS"}`,
			wantOK: true,
		},
		{
			name:   "last of several blocks wins",
			text:   "Draft:\n```json\n{\"verdict\": \"FAIL\"}\n```\nFinal:\n```json\n{\"verdict\": \"PASS\"}\n```",
			want:   `{"verdict": "PASS"}`,
			wantOK: true,
		},
		{
			name:   "invalid last block falls back to an earlier valid one",
			text:   "```json\n{\"verdict\": \"PASS\"}\n```\nand then\n```json\n{not json}\n```",
			want:   `{"verdict": "PASS"}`,
			wantOK: true,
		},
		{
			name:   "language tag is case-insensitive and CRLF is fine",
			text:   "```JSON\r\n[1, 2]\r\n```",
			want:   `[1, 2]`,
			wantOK: true,
		},
		{
			name: "other languages are ignored",
			text: "```go\nfunc main() {}\n```",
		},
		{
			name: "no block",
			text: `{"verdict": "PASS"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FencedJSON(tt.text)
			if ok != tt.wantOK || string(got) != tt.want {
				t.Errorf("FencedJSON() = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestTailKeepsTheEndOfLongMessages(t *testing.T) {
	if got := Tail("  short message \n"); got != "short message" {
		t.Errorf("Tail(short) = %q, want %q", got, "short message")
	}

	long := strings.Repeat("a", MaxMessage) + "the end"
	got := Tail(long)
	if !strings.HasSuffix(got, "the end") || !strings.HasPrefix(got, "…") {
		t.Errorf("Tail(long) should keep the end behind an ellipsis, got %q…", got[:20])
	}
	if len(got) > MaxMessage+len("…") {
		t.Errorf("len(Tail(long)) = %d, want at most %d", len(got), MaxMessage+len("…"))
	}
}

func TestErrorDescribesTheFailure(t *testing.T) {
	err := fmt.Errorf("run step: %w", &Error{
		Provider: "claude",
		Step:     "qa",
		Kind:     Timeout,
		Err:      context.DeadlineExceeded,
	})

	if want := "provider claude, step qa: timed out: context deadline exceeded"; !strings.Contains(err.Error(), want) {
		t.Errorf("Error() = %q, want it to contain %q", err.Error(), want)
	}
	var perr *Error
	if !errors.As(err, &perr) || perr.Kind != Timeout {
		t.Errorf("errors.As found %+v, want a Timeout *Error", perr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("errors.Is(err, context.DeadlineExceeded) = false, want true")
	}
}
