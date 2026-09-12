package redact

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// Fake credentials are assembled at run time so the source never contains a literal token that secret
// scanners would flag.
func fake(prefix, body string, n int) string {
	return prefix + strings.Repeat(body, n)
}

func TestStringMasksCredentialFormats(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{name: "GitHub personal access token", token: fake("ghp_", "A1b2", 9)},
		{name: "GitHub server token", token: fake("ghs_", "Z9y8", 9)},
		{name: "GitHub fine-grained token", token: fake("github_pat_", "x", 30)},
		{name: "GitLab token", token: fake("glpat-", "q", 20)},
		{name: "Anthropic API key", token: fake("sk-ant-api03-", "k", 30)},
		{name: "OpenAI project key", token: fake("sk-proj-", "o", 30)},
		{name: "AWS access key ID", token: fake("AKIA", "Q", 16)},
		{name: "Google API key", token: fake("AIza", "g", 35)},
		{name: "Slack bot token", token: fake("xoxb-", "1", 20)},
	}
	r := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.String("token=" + tt.token + " end\n")
			if want := "token=" + Mask + " end\n"; got != want {
				t.Errorf("String() = %q, want %q", got, want)
			}
		})
	}
}

func TestStringLeavesOrdinaryTextAlone(t *testing.T) {
	text := "run task-runner with --skip-checks and a desk-lamp\nnothing secret here\n"
	if got := New().String(text); got != text {
		t.Errorf("String() = %q, want it unchanged", got)
	}
}

func TestStringKeepsAuthorizationScheme(t *testing.T) {
	got := New().String("Authorization: Bearer abc.def.ghi\n")
	if want := "Authorization: Bearer " + Mask + "\n"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStringMasksPrivateKeyBlocks(t *testing.T) {
	text := "before\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\nafter\n"
	want := "before\n" + Mask + "\n" + Mask + "\n" + Mask + "\nafter\n"
	if got := New().String(text); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestNewMasksGivenValues(t *testing.T) {
	r := New("hunter2", "super-secret-value", "super-secret", "super-secret")

	tests := []struct {
		name, in, want string
	}{
		{name: "value is masked", in: "password super-secret ok", want: "password " + Mask + " ok"},
		{name: "longer value containing another is masked whole", in: "x super-secret-value y", want: "x " + Mask + " y"},
		{name: "values shorter than eight characters are ignored", in: "hunter2", want: "hunter2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.String(tt.in); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSecretValues(t *testing.T) {
	env := []string{
		"GH_TOKEN=gh-value",
		"ANTHROPIC_API_KEY=anthropic-value",
		"AWS_SECRET_ACCESS_KEY=aws-value",
		"DB_PASSWORD=db-value",
		"GOOGLE_APPLICATION_CREDENTIALS=creds-value",
		"PATH=/usr/bin",
		"KEYBOARD=us",
		"EMPTY_TOKEN=",
		"not-an-entry",
	}
	got := SecretValues(env)
	want := []string{"gh-value", "anthropic-value", "aws-value", "db-value", "creds-value"}
	if !slices.Equal(got, want) {
		t.Errorf("SecretValues() = %q, want %q", got, want)
	}
}

func TestWriterMasksSecretSplitAcrossWrites(t *testing.T) {
	token := fake("ghp_", "A1b2", 9)
	var out bytes.Buffer
	w := New().Writer(&out)

	for _, part := range []string{"auth " + token[:5], token[5:20], token[20:] + " done\nnext"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if want := "auth " + Mask + " done\nnext"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestWriterMasksPrivateKeyAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	w := New().Writer(&out)
	for _, part := range []string{"-----BEGIN RSA PRIVATE", " KEY-----\nMIIE", "vQIBADANBg\n-----END RSA PRIVATE KEY-----\nok\n"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	want := Mask + "\n" + Mask + "\n" + Mask + "\nok\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestWriterHoldsBackPartialLinesUntilClose(t *testing.T) {
	var out bytes.Buffer
	w := New().Writer(&out)
	if _, err := w.Write([]byte("no newline yet")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output before Close = %q, want nothing", out.String())
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if out.String() != "no newline yet" {
		t.Errorf("output after Close = %q, want %q", out.String(), "no newline yet")
	}
}

func TestWriterFlushesOverlongLines(t *testing.T) {
	var out bytes.Buffer
	w := New().Writer(&out)
	long := strings.Repeat("a", maxLine+10)

	n, err := w.Write([]byte(long))
	if err != nil || n != len(long) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(long))
	}
	if out.Len() != len(long) {
		t.Errorf("wrote %d bytes before Close, want all %d once the line limit was reached", out.Len(), len(long))
	}
}
