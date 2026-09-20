package prompt

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stritech-oss/loomlc/internal/config"
)

func TestRoleReturnsEveryBuiltInRole(t *testing.T) {
	for _, name := range Roles() {
		text, ok := Role(name)
		if !ok {
			t.Fatalf("Role(%q) is missing", name)
		}
		if !strings.Contains(text, "JSON matching the schema") {
			t.Errorf("role %q doesn't tell the agent what to answer with", name)
		}
		if len(text) > DefaultMaxRole {
			t.Errorf("role %q is %d bytes, over the %d-byte limit providers allow", name, len(text), DefaultMaxRole)
		}
	}
	if _, ok := Role("architect"); ok {
		t.Error(`Role("architect") = ok, want it to be unknown`)
	}
}

func TestLoaderRole(t *testing.T) {
	files := map[string]string{
		"prompts/reviewer.md": "You review things.\n",
		"prompts/empty.md":    "  \n",
		"prompts/big.md":      strings.Repeat("x", 200),
	}
	l := Loader{
		MaxBytes: 100,
		ReadFile: func(name string) ([]byte, error) {
			text, ok := files[name]
			if !ok {
				return nil, fmt.Errorf("open %s: %w", name, os.ErrNotExist)
			}
			return []byte(text), nil
		},
	}

	if got, err := l.Role("prompts/reviewer.md"); err != nil || got != files["prompts/reviewer.md"] {
		t.Errorf("Role(custom) = %q, %v", got, err)
	}
	if got, err := l.Role(RoleQA); err != nil || !strings.Contains(got, "review agent") {
		t.Errorf("Role(qa) = %q, %v; want the built-in role", got, err)
	}

	cases := []struct{ name, role, want string }{
		{"missing file", "prompts/nope.md", "read custom role"},
		{"empty file", "prompts/empty.md", "is empty"},
		{"over the limit", "prompts/big.md", "over the 100-byte limit"},
		{"no role", "", "a step needs a role"},
		{"not a prompt", "architect", "unknown role"},
		{"absolute path", "/etc/prompt.md", "inside the repository"},
		{"outside the repository", "../secrets/prompt.md", "inside the repository"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := l.Role(c.role); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Role(%q) = %v, want an error about %q", c.role, err, c.want)
			}
		})
	}

	if _, err := (Loader{}).Role("prompts/reviewer.md"); err == nil {
		t.Error("a Loader with no ReadFile accepted a custom role")
	}
}

func TestSchemasAreStrict(t *testing.T) {
	for _, output := range []string{OutputPlan, OutputChange, OutputVerdict} {
		t.Run(output, func(t *testing.T) {
			raw, err := Schema(output)
			if err != nil {
				t.Fatalf("Schema: %v", err)
			}
			if strings.ContainsAny(string(raw), "\n\t") {
				t.Error("the schema isn't compact, so providers send more than they need to")
			}

			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// checkStrict only has rules for objects, so a schema that forgot to say it is one would
			// otherwise be waved through with nothing checked at all.
			if schema["type"] != "object" {
				t.Fatalf(`top level is %v, want "object"`, schema["type"])
			}
			checkStrict(t, output, schema)
		})
	}
	if _, err := Schema("summary"); err == nil {
		t.Error(`Schema("summary") = nil error, want an unknown output error`)
	}
}

// The schemas carry no $schema keyword: claude 2.1.270 refuses one it can't resolve, with
// "--json-schema is not a valid JSON Schema: no schema with key or ref
// "https://json-schema.org/draft/2020-12/schema"". See testdata/README.md.
func TestSchemasOmitTheDialectKeyword(t *testing.T) {
	for _, output := range []string{OutputPlan, OutputChange, OutputVerdict} {
		raw, err := Schema(output)
		if err != nil {
			t.Fatalf("Schema(%q): %v", output, err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, ok := schema["$schema"]; ok {
			t.Errorf("%s declares $schema, which claude refuses to resolve", output)
		}
	}
}

// checkStrict walks a schema and holds every object to the rules structured output needs: no extra
// properties, and every property required, so a model can't answer by omission.
func checkStrict(t *testing.T, at string, schema map[string]any) {
	t.Helper()
	if schema["type"] != "object" {
		return
	}
	if schema["additionalProperties"] != false {
		t.Errorf("%s: allows extra properties", at)
	}
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	if len(props) != len(required) {
		t.Errorf("%s: has %d properties but requires %d; structured output needs every field required", at, len(props), len(required))
	}
	for _, name := range required {
		if _, ok := props[name.(string)]; !ok {
			t.Errorf("%s: requires %q, which isn't a property", at, name)
		}
	}
	for name, value := range props {
		prop, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if prop["description"] == nil {
			t.Errorf("%s.%s: has no description, so the model has to guess what it means", at, name)
		}
		checkStrict(t, at+"."+name, prop)
		if items, ok := prop["items"].(map[string]any); ok {
			checkStrict(t, at+"."+name+"[]", items)
		}
	}
}

// The schemas answer for the outputs config knows about, under the same names.
func TestOutputNamesMatchConfig(t *testing.T) {
	for _, output := range []string{config.OutputPlan, config.OutputChange, config.OutputVerdict} {
		if _, err := Schema(output); err != nil {
			t.Errorf("config output %q has no schema: %v", output, err)
		}
	}
	for _, name := range []string{OutputPlan, OutputChange, OutputVerdict} {
		if name != config.OutputPlan && name != config.OutputChange && name != config.OutputVerdict {
			t.Errorf("output %q isn't one config uses", name)
		}
	}
}

// Every role this package ships is one a configuration can actually name. Adding a role here and
// forgetting config would leave it rejected as unknown before a step ever reached this package.
func TestEveryBuiltInRoleIsConfigurable(t *testing.T) {
	outputs := map[string]string{RolePlanner: OutputPlan, RoleEngineer: OutputChange, RoleQA: OutputVerdict}
	for _, role := range Roles() {
		yaml := fmt.Sprintf("lifecycles:\n  sdlc:\n    steps:\n      - name: plan\n        role: %s\n", role)
		cfg, err := config.Parse("loomlc.yml", []byte(yaml))
		if err != nil && strings.Contains(err.Error(), "role") {
			t.Errorf("config rejects the built-in role %q: %v", role, err)
			continue
		}
		if err != nil {
			continue // the one-step lifecycle fails other rules; only the role matters here
		}
		if got := cfg.Lifecycles["sdlc"].Steps[0].Output; got != outputs[role] {
			t.Errorf("config gives role %q the output %q, this package gives it %q", role, got, outputs[role])
		}
	}
}

// The preset's roles are the ones this package ships, so the built-in lifecycle runs without a custom
// prompt file.
func TestPresetRolesAreBuiltIn(t *testing.T) {
	cfg, err := config.Parse("loomlc.yml", nil)
	if err != nil {
		t.Fatalf("load the preset: %v", err)
	}
	for name, lifecycle := range cfg.Lifecycles {
		for _, step := range lifecycle.Steps {
			if _, ok := Role(step.Role); !ok {
				t.Errorf("preset lifecycle %s, step %s: role %q isn't built in", name, step.Name, step.Role)
			}
			if _, err := Schema(step.Output); err != nil {
				t.Errorf("preset lifecycle %s, step %s: %v", name, step.Name, err)
			}
		}
	}
}
