package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stritech-oss/loomlc/internal/redact"
)

func record() Run {
	return Run{
		ID:        "run-1",
		Lifecycle: "sdlc",
		StartedAt: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC),
		Task:      Task{Ref: "github#9", Title: "Print the build commit", URL: "https://github.com/acme/widgets/issues/9"},
		Branch:    "feat/issue-9",
		Base:      "origin/main",
		Outcome:   "proposed",
		Passes:    2,
		Commits:   []Commit{{SHA: "1111111", Subject: "feat(cli): print the build commit"}},
		Gates:     []Gate{{Name: "task check", Passed: true}},
		Output:    &Output{Ref: "github-pr#45", URL: "https://github.com/acme/widgets/pull/45"},
	}
}

func TestSaveWritesTheRecord(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, "run-1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Save(record()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(root, "run-1", "run.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Run
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Outcome != "proposed" || got.Passes != 2 || got.Output.URL == "" || got.Task.Ref != "github#9" {
		t.Errorf("record = %+v", got)
	}

	// Saving again replaces it, so an interrupted run leaves the last thing loomlc knew.
	second := record()
	second.Outcome, second.Error = "", "the provider timed out"
	if err := w.Save(second); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ = os.ReadFile(filepath.Join(root, "run-1", "run.json"))
	if !strings.Contains(string(body), "the provider timed out") || strings.Contains(string(body), `"outcome": "proposed"`) {
		t.Errorf("the record wasn't replaced:\n%s", body)
	}
}

func TestStepFilesAreNumberedInOrder(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, "run-1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, step := range []string{"plan", "engineer", "qa"} {
		if err := w.Step(step, "the prompt for "+step, `{"status":"ok"}`); err != nil {
			t.Fatalf("Step: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, "run-1"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	want := []string{
		"01-plan-answer.json", "01-plan-prompt.md",
		"02-engineer-answer.json", "02-engineer-prompt.md",
		"03-qa-answer.json", "03-qa-prompt.md",
	}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("files = %q, want them numbered in the order they ran", names)
	}
}

// A prompt carries the task's own words and an answer carries whatever an agent read, so both are masked
// before they land in a file an operator opens.
func TestWhatIsWrittenIsRedacted(t *testing.T) {
	const secret = "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	root := t.TempDir()
	w, err := New(root, "run-1", redact.New("the-operators-own-secret"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Step("engineer", "the token is "+secret, `{"summary":"used the-operators-own-secret"}`); err != nil {
		t.Fatalf("Step: %v", err)
	}
	r := record()
	r.Error = "failed with " + secret
	if err := w.Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, name := range []string{"01-engineer-prompt.md", "01-engineer-answer.json", "run.json"} {
		body, err := os.ReadFile(filepath.Join(root, "run-1", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, leak := range []string{secret, "the-operators-own-secret"} {
			if strings.Contains(string(body), leak) {
				t.Errorf("%s contains a secret", name)
			}
		}
		if !strings.Contains(string(body), redact.Mask) {
			t.Errorf("%s has no mask, so nothing was redacted:\n%s", name, body)
		}
	}
}

func TestNewRefusesARunIDThatIsNotADirectoryName(t *testing.T) {
	for _, id := range []string{"", "..", "../escape", "run/1", ".hidden"} {
		if _, err := New(t.TempDir(), id, nil); err == nil {
			t.Errorf("New accepted the run id %q", id)
		}
	}
}

func TestDirSaysWhereToLook(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, "run-1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.Dir() != filepath.Join(root, "run-1") {
		t.Errorf("Dir = %q", w.Dir())
	}
	if _, err := os.Stat(w.Dir()); err != nil {
		t.Errorf("the directory wasn't created: %v", err)
	}
}
