// Package prompt supplies what a step needs to run: the built-in role instructions, the JSON Schema its
// answer must match, and the task prompt loomlc builds for it.
//
// Role instructions and templates are trusted text that ships with loomlc. Everything else is not: a
// task's description, a reviewer's comment, and an earlier step's answer, which is a model's words
// about untrusted material and reaches the next step with none of loomlc's authority. Render fences
// all of it, escaping any spelling of the fence's own tag so quoted text can't break out, and flattens
// short forge-supplied values such as an author's name so they can't add lines to the prompt.
//
// The schemas declare no $schema dialect: claude refuses a schema whose dialect it can't resolve. What
// was checked against a real CLI is recorded in testdata/README.md.
package prompt

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Built-in roles. A lifecycle step names one of these or a path to its own prompt.
const (
	RolePlanner  = "planner"
	RoleEngineer = "engineer"
	RoleQA       = "qa"
)

// Step outputs, matching the values config uses.
const (
	OutputPlan    = "plan"
	OutputChange  = "change"
	OutputVerdict = "verdict"
)

//go:embed roles/*.md schema/*.json
var files embed.FS

// Role returns a built-in role's instructions.
func Role(name string) (string, bool) {
	switch name {
	case RolePlanner, RoleEngineer, RoleQA:
	default:
		return "", false
	}
	text, err := files.ReadFile("roles/" + name + ".md")
	if err != nil {
		// The roles are embedded, so a missing one is a build problem, not a runtime one.
		panic("prompt: built-in role " + name + " is missing: " + err.Error())
	}
	return string(text), true
}

// Roles lists the built-in roles.
func Roles() []string { return []string{RolePlanner, RoleEngineer, RoleQA} }

// Loader resolves a step's role to its instructions. A role that isn't built in is a path to a prompt in
// the operator's checkout, read through ReadFile so it never comes from the workspace an agent can edit.
type Loader struct {
	// ReadFile reads a file from the operator's checkout, such as os.ReadFile with the checkout joined in.
	ReadFile func(name string) ([]byte, error)
	// MaxBytes bounds a custom prompt. Zero means DefaultMaxRole.
	MaxBytes int
}

// DefaultMaxRole bounds a custom role prompt. Providers such as claude pass the role as a command-line
// argument, which Linux caps at 128 KiB.
const DefaultMaxRole = 64 << 10

// Role returns the instructions for a step's role.
func (l Loader) Role(role string) (string, error) {
	if text, ok := Role(role); ok {
		return text, nil
	}
	if role == "" {
		return "", fmt.Errorf("prompt: a step needs a role: %s, or a path to a .md prompt", strings.Join(Roles(), ", "))
	}
	if !strings.HasSuffix(role, ".md") {
		return "", fmt.Errorf("prompt: unknown role %q: use %s, or a path to a .md prompt", role, strings.Join(Roles(), ", "))
	}
	if l.ReadFile == nil {
		return "", fmt.Errorf("prompt: no way to read the custom role %q", role)
	}
	// This is the test config runs on the same path, down to ".." counting only as a whole segment: a
	// file honestly named "..md" is inside the repository, and a role that passed validation mustn't
	// then be refused here. See checkRelativePath in internal/config.
	clean := path.Clean(filepath.ToSlash(role))
	if path.IsAbs(role) || filepath.IsAbs(role) || slices.Contains(strings.Split(clean, "/"), "..") {
		return "", fmt.Errorf("prompt: custom role %q must be inside the repository", role)
	}
	text, err := l.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("prompt: read custom role %q: %w", role, err)
	}
	limit := l.MaxBytes
	if limit == 0 {
		limit = DefaultMaxRole
	}
	if len(text) > limit {
		return "", fmt.Errorf("prompt: custom role %q is %d bytes, over the %d-byte limit", role, len(text), limit)
	}
	if len(bytes.TrimSpace(text)) == 0 {
		return "", fmt.Errorf("prompt: custom role %q is empty", role)
	}
	return string(text), nil
}

// Schema returns the JSON Schema a step's answer must match, compacted so providers that pass it as a
// command-line argument send as little as possible.
func Schema(output string) (json.RawMessage, error) {
	switch output {
	case OutputPlan, OutputChange, OutputVerdict:
	default:
		return nil, fmt.Errorf("prompt: unknown step output %q: want %s, %s, or %s", output, OutputPlan, OutputChange, OutputVerdict)
	}
	raw, err := files.ReadFile("schema/" + output + ".json")
	if err != nil {
		panic("prompt: built-in schema " + output + " is missing: " + err.Error())
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		panic("prompt: built-in schema " + output + " isn't valid JSON: " + err.Error())
	}
	return json.RawMessage(compact.Bytes()), nil
}
