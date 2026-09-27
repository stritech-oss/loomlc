package run

import (
	"os"
	"path/filepath"
	"testing"
)

// testdata/real.diff came out of git 2.43, so the parsing is checked against what git actually emits —
// hunk headers carrying trailing context, an "index" line, and a second file in the same patch.
func TestScanReadsARealDiff(t *testing.T) {
	patch, err := os.ReadFile(filepath.Join("testdata", "real.diff"))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}

	got := NewSecrets(nil, nil).Scan(string(patch))
	if len(got) != 1 {
		t.Fatalf("matches = %+v, want one", got)
	}
	if got[0].Path != "config.go" || got[0].Line != 6 {
		t.Errorf("match = %+v, want config.go:6, where the line actually lands in the new file", got[0])
	}
}
