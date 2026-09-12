package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/version"
)

func TestMain(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // exact match
		wantStderr string // substring; empty means stderr must be empty
	}{
		{
			name:       "no command prints usage to stderr",
			args:       nil,
			wantCode:   ExitUsage,
			wantStderr: "Usage:",
		},
		{
			name:       "help prints usage to stdout",
			args:       []string{"help"},
			wantCode:   ExitOK,
			wantStdout: usage,
		},
		{
			name:       "version prints the build version",
			args:       []string{"version"},
			wantCode:   ExitOK,
			wantStdout: "loomlc " + version.Version + "\n",
		},
		{
			name:       "version rejects arguments",
			args:       []string{"version", "extra"},
			wantCode:   ExitUsage,
			wantStderr: `unexpected arguments ["extra"]`,
		},
		{
			name:       "unknown command is a usage error",
			args:       []string{"frobnicate"},
			wantCode:   ExitUsage,
			wantStderr: `unknown command "frobnicate"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Main(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tt.wantStdout)
			}
			gotErr := stderr.String()
			if tt.wantStderr == "" && gotErr != "" {
				t.Errorf("stderr = %q, want empty", gotErr)
			}
			if !strings.Contains(gotErr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", gotErr, tt.wantStderr)
			}
		})
	}
}
