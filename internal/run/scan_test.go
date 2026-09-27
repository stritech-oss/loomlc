package run

import (
	"fmt"
	"strings"
	"testing"
)

// token is assembled at run time so this file never contains a literal a scanner would flag.
func token() string { return "ghp_" + strings.Repeat("A1b2", 9) }

func diff(path string, added ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,2 +1,%d @@\n context\n", path, path, path, path, len(added)+1)
	for _, line := range added {
		fmt.Fprintf(&b, "+%s\n", line)
	}
	return b.String()
}

func TestScanFindsCredentialsInAddedLines(t *testing.T) {
	s := NewSecrets([]string{"GH_TOKEN=s3cret-value-from-the-run"}, nil)

	tests := []struct {
		name  string
		line  string
		rule  string
		line0 int
	}{
		{name: "a token format", line: "client := newClient(\"" + token() + "\")", rule: "shaped like a credential", line0: 2},
		{name: "the run's own secret", line: "const fallback = \"s3cret-value-from-the-run\"", rule: "from this run's own environment", line0: 2},
		{name: "ordinary code", line: "client := newClient(os.Getenv(\"GH_TOKEN\"))", rule: "", line0: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.Scan(diff("internal/cli/client.go", tt.line))
			if tt.rule == "" {
				if len(got) != 0 {
					t.Fatalf("matches = %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("matches = %+v, want one", got)
			}
			if got[0].Path != "internal/cli/client.go" || got[0].Line != tt.line0 {
				t.Errorf("match = %+v, want the file and the line it was added on", got[0])
			}
			if !strings.Contains(got[0].Rule, tt.rule) {
				t.Errorf("rule = %q, want it to mention %q", got[0].Rule, tt.rule)
			}
			if strings.Contains(got[0].Rule, tt.line) {
				t.Error("the rule repeats the secret it found")
			}
		})
	}
}

// A secret being taken out is the good case.
func TestScanIgnoresRemovedLines(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,1 @@\n context\n-const key = \"" + token() + "\"\n"
	if got := NewSecrets(nil, nil).Scan(patch); len(got) != 0 {
		t.Errorf("matches = %+v, want none: the line was deleted", got)
	}
}

func TestScanCountsLinesAcrossHunks(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n" +
		"@@ -1,1 +1,2 @@\n context\n+fine\n" +
		"@@ -40,1 +41,2 @@\n context\n+const key = \"" + token() + "\"\n"

	got := NewSecrets(nil, nil).Scan(patch)
	if len(got) != 1 {
		t.Fatalf("matches = %+v, want one", got)
	}
	if got[0].Line != 42 {
		t.Errorf("line = %d, want 42: the second hunk starts at 41", got[0].Line)
	}
}

// A repository with credential-shaped fixtures says so, or the gate can never be mandatory.
func TestScanSkipsExemptPaths(t *testing.T) {
	patch := diff("internal/redact/testdata/tokens.txt", "const key = \""+token()+"\"")
	if got := NewSecrets(nil, []string{"internal/redact/testdata"}).Scan(patch); len(got) != 0 {
		t.Errorf("matches = %+v, want none: the path is exempt", got)
	}
	if got := NewSecrets(nil, nil).Scan(patch); len(got) != 1 {
		t.Errorf("matches = %+v, want one without the exemption", got)
	}
}

func TestScanIgnoresADeletedFile(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-const key = \"" + token() + "\"\n"
	if got := NewSecrets(nil, nil).Scan(patch); len(got) != 0 {
		t.Errorf("matches = %+v, want none", got)
	}
}
