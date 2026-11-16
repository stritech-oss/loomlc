package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWarningsAreKeptInTheRecord(t *testing.T) {
	root := t.TempDir()
	w, err := New(root, "run-1", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := record()
	if err := w.Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "run-1", "run.json"))
	if strings.Contains(string(body), `"warnings"`) {
		t.Errorf("a run with no warnings wrote the key:\n%s", body)
	}

	r.Warnings = []string{"couldn't request a review from pedoch"}
	if err := w.Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ = os.ReadFile(filepath.Join(root, "run-1", "run.json"))
	var got Run
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !slices.Equal(got.Warnings, r.Warnings) {
		t.Errorf("warnings = %q, want %q", got.Warnings, r.Warnings)
	}
}
