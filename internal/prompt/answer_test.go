package prompt

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The Go types and the schemas describe the same answer. A field added to one and not the other means a
// step's answer is silently thrown away, or asked for and never read.
func TestAnswerTypesMatchTheSchemas(t *testing.T) {
	tests := []struct {
		output string
		answer any
	}{
		{OutputPlan, Plan{}},
		{OutputChange, Change{}},
		{OutputVerdict, Verdict{}},
	}
	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			raw, err := Schema(tt.output)
			if err != nil {
				t.Fatalf("Schema: %v", err)
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			compareFields(t, tt.output, schema, reflect.TypeOf(tt.answer))
		})
	}
}

// compareFields checks a schema object and a Go struct describe the same fields, walking into nested objects.
func compareFields(t *testing.T, at string, schema map[string]any, typ reflect.Type) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)

	fields := map[string]reflect.Type{}
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" {
			t.Errorf("%s: field %s has no json tag", at, field.Name)
			continue
		}
		fields[name] = field.Type
	}

	for name := range props {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s: the schema asks for %q and the Go type has no field for it", at, name)
		}
	}
	for name := range fields {
		if _, ok := props[name]; !ok {
			t.Errorf("%s: the Go type reads %q and the schema never asks for it", at, name)
		}
	}

	for name, value := range props {
		prop, ok := value.(map[string]any)
		if !ok {
			continue
		}
		field, ok := fields[name]
		if !ok {
			continue
		}
		items, hasItems := prop["items"].(map[string]any)
		switch {
		case hasItems && items["type"] == "object" && field.Kind() == reflect.Slice:
			compareFields(t, at+"."+name+"[]", items, field.Elem())
		case prop["type"] == "object" && field.Kind() == reflect.Struct:
			compareFields(t, at+"."+name, prop, field)
		}
	}
}

func TestAnswerReadsAStepsAnswer(t *testing.T) {
	var plan Plan
	raw := json.RawMessage(`{"status":"ready","pr_title":"feat(cli): print the build commit","summary":"Prints it.","files":[{"path":"a.go","change":"add the variable"}],"tests":["the unstamped default"],"deferred":[],"blockers":[]}`)
	if err := Answer(OutputPlan, raw, &plan); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if plan.Blocked() || plan.PRTitle == "" || len(plan.Files) != 1 || plan.Files[0].Path != "a.go" {
		t.Errorf("plan = %+v", plan)
	}

	var change Change
	raw = json.RawMessage(`{"status":"complete","summary":"Did it.","commits":[{"message":"feat: a","paths":["a.go"],"fixes":""}],"addressed":[],"blockers":[]}`)
	if err := Answer(OutputChange, raw, &change); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !change.Complete() || len(change.Commits) != 1 || change.Commits[0].Paths[0] != "a.go" {
		t.Errorf("change = %+v", change)
	}

	var verdict Verdict
	raw = json.RawMessage(`{"verdict":"fail","summary":"Not yet.","findings":[{"detail":"no test","path":"a.go","line":4}]}`)
	if err := Answer(OutputVerdict, raw, &verdict); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if verdict.Passed() || len(verdict.Findings) != 1 || verdict.Findings[0].Line != 4 {
		t.Errorf("verdict = %+v", verdict)
	}
}

// A provider that can't enforce a schema falls back to a fenced block, so the values are checked again
// here rather than trusted.
func TestAnswerRejectsWhatItCannotAct0n(t *testing.T) {
	tests := []struct {
		name   string
		output string
		raw    string
		into   any
		want   string
	}{
		{name: "no answer", output: OutputPlan, raw: "", into: &Plan{}, want: "returned no answer"},
		{name: "not the right shape", output: OutputPlan, raw: `{"status":["ready"]}`, into: &Plan{}, want: "isn't the shape"},
		{name: "a status nobody defined", output: OutputPlan, raw: `{"status":"maybe"}`, into: &Plan{}, want: `is "maybe"`},
		{name: "a change status nobody defined", output: OutputChange, raw: `{"status":"done"}`, into: &Change{}, want: `is "done"`},
		{name: "a verdict nobody defined", output: OutputVerdict, raw: `{"verdict":"PASS"}`, into: &Verdict{}, want: `is "PASS"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Answer(tt.output, json.RawMessage(tt.raw), tt.into)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Answer = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// The outputs this package decodes are the ones it has schemas for.
func TestEveryOutputHasAnAnswerType(t *testing.T) {
	outputs := []string{OutputPlan, OutputChange, OutputVerdict}
	for _, output := range outputs {
		if _, err := Schema(output); err != nil {
			t.Errorf("%s has no schema: %v", output, err)
		}
	}
	if !slices.Contains(outputs, OutputChange) {
		t.Error("the change output isn't in the list this test checks")
	}
}
