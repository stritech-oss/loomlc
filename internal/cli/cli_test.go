package cli

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/version"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata")

// fakeFiles returns a ReadFile that serves files from memory. A missing file is fs.ErrNotExist, like
// os.ReadFile.
func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		content, ok := files[name]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		return []byte(content), nil
	}
}

type result struct {
	code           int
	stdout, stderr string
}

func run(args []string, files map[string]string) result {
	var stdout, stderr bytes.Buffer
	code := Main(args, Env{Stdout: &stdout, Stderr: &stderr, ReadFile: fakeFiles(files)})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
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
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read golden file (run go test with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func TestMainDispatchesCommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // exact match
		wantStderr string // substring; empty means stderr must be empty
	}{
		{name: "no command prints usage to stderr", args: nil, wantCode: ExitUsage, wantStderr: "Usage:"},
		{name: "help prints usage to stdout", args: []string{"help"}, wantCode: ExitOK, wantStdout: usage},
		{name: "version prints the build version", args: []string{"version"}, wantCode: ExitOK, wantStdout: "loomlc " + version.Version + "\n"},
		{name: "version rejects arguments", args: []string{"version", "extra"}, wantCode: ExitUsage, wantStderr: `unexpected arguments ["extra"]`},
		{name: "unknown command is a usage error", args: []string{"frobnicate"}, wantCode: ExitUsage, wantStderr: `unknown command "frobnicate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(tt.args, nil)
			if got.code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", got.code, tt.wantCode)
			}
			if got.stdout != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.wantStdout)
			}
			if tt.wantStderr == "" && got.stderr != "" {
				t.Errorf("stderr = %q, want empty", got.stderr)
			}
			if !strings.Contains(got.stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, tt.wantStderr)
			}
		})
	}
}

func TestLifecyclesPrintsThePresetWithoutAConfigFile(t *testing.T) {
	got := run([]string{"lifecycles"}, nil)
	if got.code != ExitOK || got.stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	golden(t, "lifecycles_preset", got.stdout)
}

func TestLifecyclesPrintsTheMergedConfigFile(t *testing.T) {
	files := map[string]string{"loomlc.yml": `lifecycles:
  sdlc:
    concurrency: 1
    steps:
      - name: plan
        provider: pi
        vendor: google
        model: gemini-3-pro
      - name: qa
        provider: codex
        model: gpt-5-codex
        gate: ["go vet ./...", [go, test, -race, ./...]]
`}
	got := run([]string{"lifecycles"}, files)
	if got.code != ExitOK || got.stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	golden(t, "lifecycles_merged", got.stdout)
}

func TestLifecyclesReadsTheConfigFlag(t *testing.T) {
	files := map[string]string{"ops/loomlc.yml": "lifecycles:\n  sdlc:\n    concurrency: 2\n"}
	got := run([]string{"lifecycles", "--config", "ops/loomlc.yml"}, files)
	if got.code != ExitOK {
		t.Fatalf("exit code = %d, stderr = %q", got.code, got.stderr)
	}
	for _, want := range []string{"Configuration: ops/loomlc.yml merged over", "2 at a time"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", got.stdout, want)
		}
	}
}

func TestLifecyclesReportsConfigErrors(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		files      map[string]string
		wantStderr string
	}{
		{name: "missing --config file", args: []string{"lifecycles", "--config", "nope.yml"}, wantStderr: "read configuration: open nope.yml"},
		{name: "invalid configuration", args: []string{"lifecycles"}, files: map[string]string{"loomlc.yml": "lifecycles:\n  sdlc:\n    concurrency: 0\n"}, wantStderr: "lifecycles.sdlc.concurrency: must be at least 1"},
		{name: "unexpected argument", args: []string{"lifecycles", "sdlc"}, wantStderr: `unexpected arguments ["sdlc"]`},
		{name: "unknown flag", args: []string{"lifecycles", "--verbose"}, wantStderr: "flag provided but not defined: -verbose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(tt.args, tt.files)
			if got.code != ExitUsage {
				t.Errorf("exit code = %d, want %d", got.code, ExitUsage)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want empty", got.stdout)
			}
			if !strings.Contains(got.stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, tt.wantStderr)
			}
		})
	}
}

func TestLifecyclesReportsUnreadableDefaultFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	denied := func(string) ([]byte, error) { return nil, fs.ErrPermission }

	code := Main([]string{"lifecycles"}, Env{Stdout: &stdout, Stderr: &stderr, ReadFile: denied})
	if code != ExitUsage || !strings.Contains(stderr.String(), "read configuration: permission denied") {
		t.Errorf("exit code = %d, stderr = %q; want %d and a read error", code, stderr.String(), ExitUsage)
	}
}
