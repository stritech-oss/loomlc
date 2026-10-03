package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sections() Sections {
	return Sections{
		Description: []string{"Done / Description", "Description", "Summary"},
		QA:          []string{"QA", "Testing"},
		Issue:       []string{"Issue / Card", "Issue", "Related issues"},
	}
}

// The repository's own template is the one this has to fill.
func template(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "pull_request_template.md"))
	if err != nil {
		t.Skipf("no pull request template: %v", err)
	}
	return string(b)
}

func TestBodyFillsTheTemplatesSections(t *testing.T) {
	body, err := Body(template(t), sections(), Parts{
		Description: "Prints the commit the binary was built from.",
		QA:          "task check passes; a test covers the unstamped default.",
		Issue:       "Closes #9",
	})
	if err != nil {
		t.Fatalf("Body: %v", err)
	}

	for _, want := range []string{
		"## Done / Description\n\nPrints the commit the binary was built from.",
		"## QA\n\ntask check passes",
		"## Issue / Card\n\nCloses #9",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body doesn't contain %q:\n%s", want, body)
		}
	}
}

// The checklist is for the person who merges. loomlc doesn't tick boxes on their behalf.
func TestBodyLeavesTheChecklistAlone(t *testing.T) {
	body, err := Body(template(t), sections(), Parts{Description: "d", QA: "q", Issue: "i"})
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if strings.Contains(body, "- [x]") {
		t.Error("loomlc ticked a checklist box")
	}
	if !strings.Contains(body, "- [ ] Every commit is signed off by its author") {
		t.Error("the checklist was dropped")
	}
	if !strings.Contains(body, "### Change") {
		t.Error("a heading loomlc doesn't fill was dropped")
	}
}

// A reader has to be told before they read the description, not after.
func TestBodyPutsWarningsFirst(t *testing.T) {
	body, err := Body(template(t), sections(), Parts{
		Description: "Prints it.",
		Warnings:    []string{"No checks ran: nothing built or tested this change."},
	})
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.HasPrefix(body, "> **No checks ran") {
		t.Errorf("body starts with:\n%s", body[:min(len(body), 120)])
	}
	if strings.Index(body, "No checks ran") > strings.Index(body, "Prints it.") {
		t.Error("the warning comes after the description")
	}
}

func TestBodyRefusesATemplateItCannotFill(t *testing.T) {
	if _, err := Body("", sections(), Parts{}); err == nil {
		t.Error("an empty template was accepted")
	}
	if _, err := Body("## Notes\n\nnothing to fill here\n", sections(), Parts{Description: "d"}); err == nil {
		t.Error("a template with none of the headings was accepted; the description would have been dropped")
	}
}

// A section with nothing to say says so, rather than leaving a template's instructions in place as if a
// person had written them.
func TestBodySaysWhenASectionHasNothing(t *testing.T) {
	body, err := Body(template(t), sections(), Parts{Description: "d", QA: ""})
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(body, "## QA\n\n_Nothing to report._") {
		t.Errorf("empty QA section:\n%s", body)
	}
	if strings.Contains(body, "How you verified the change") {
		t.Error("the template's instructions to a person were left in a section loomlc filled")
	}
}

// A template can call its sections whatever it likes, and matching ignores case.
func TestBodyMatchesTheRepositorysOwnHeadings(t *testing.T) {
	custom := "## summary\n\n<!-- what -->\n\n## testing\n\n<!-- how -->\n"
	body, err := Body(custom, sections(), Parts{Description: "what it does", QA: "how it was checked"})
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.Contains(body, "## summary\n\nwhat it does") || !strings.Contains(body, "## testing\n\nhow it was checked") {
		t.Errorf("body = %q", body)
	}
}

// Anything before the first heading belongs to the template's author.
func TestBodyKeepsAPreamble(t *testing.T) {
	body, err := Body("Thanks for contributing!\n\n## QA\n\n<!-- how -->\n", sections(), Parts{QA: "checked"})
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if !strings.HasPrefix(body, "Thanks for contributing!") {
		t.Errorf("body = %q", body)
	}
}
