package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// PresetName identifies the built-in preset in messages.
const PresetName = "the built-in sdlc preset"

//go:embed preset/sdlc.yml
var preset []byte

// Parse merges the loomlc.yml content in data over the built-in preset, fills in defaults, and
// validates the result. name identifies data in error messages. Empty data, or data with only comments,
// yields the preset on its own.
func Parse(name string, data []byte) (*Config, error) {
	merged, err := decodeDocument(PresetName, preset)
	if err != nil {
		return nil, err
	}

	over, err := decodeDocument(name, data)
	switch {
	case err != nil:
		return nil, err
	case over != nil:
		merged = mergeNode(merged, over, nil)
	default:
		name = PresetName
	}

	var c Config
	if err := merged.Decode(&c); err != nil {
		return nil, fmt.Errorf("load %s: %w", name, err)
	}
	applyDefaults(&c)
	if err := validate(&c); err != nil {
		return nil, fmt.Errorf("invalid configuration in %s:\n%w", name, err)
	}
	return &c, nil
}

// decodeDocument parses data, rejecting keys the schema doesn't define, and returns its top-level
// mapping. It returns nil for a document with no content.
func decodeDocument(name string, data []byte) (*yaml.Node, error) {
	// Decoding into Config first catches unknown keys and bad values with line numbers from this file,
	// before merging makes the lines meaningless.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var strict Config
	if err := dec.Decode(&strict); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}

	// Only the first document is read, so a second one would be dropped along with any mistake in it.
	var rest Config
	if err := dec.Decode(&rest); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: loomlc reads one configuration; remove the second YAML document after \"---\"", name)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s: the top level must be a mapping of settings", name)
	}
	return doc.Content[0], nil
}

// mergeNode overlays over onto base without modifying either. Mappings merge key by key, and a
// lifecycle's steps merge by step name. Anything else in over, including other lists, replaces the value
// in base. path holds the mapping keys that lead to the nodes.
func mergeNode(base, over *yaml.Node, path []string) *yaml.Node {
	switch {
	case base.Kind == yaml.MappingNode && over.Kind == yaml.MappingNode:
		return mergeMapping(base, over, path)
	case base.Kind == yaml.SequenceNode && over.Kind == yaml.SequenceNode && isStepList(path):
		return mergeSteps(base, over, path)
	default:
		return over
	}
}

// isStepList reports whether path leads to a lifecycle's steps.
func isStepList(path []string) bool {
	return len(path) == 3 && path[0] == "lifecycles" && path[2] == "steps"
}

func mergeMapping(base, over *yaml.Node, path []string) *yaml.Node {
	out := *base
	out.Content = slices.Clone(base.Content)
	for i := 0; i+1 < len(over.Content); i += 2 {
		key, value := over.Content[i], over.Content[i+1]
		j := mappingIndex(&out, key.Value)
		if j < 0 {
			out.Content = append(out.Content, key, value)
			continue
		}
		out.Content[j+1] = mergeNode(out.Content[j+1], value, append(slices.Clone(path), key.Value))
	}
	return &out
}

// mergeSteps merges each step in over into the base step with the same name, or appends it.
func mergeSteps(base, over *yaml.Node, path []string) *yaml.Node {
	out := *base
	out.Content = slices.Clone(base.Content)
	for _, step := range over.Content {
		name := scalarValue(step, "name")
		i := slices.IndexFunc(out.Content, func(s *yaml.Node) bool {
			return name != "" && scalarValue(s, "name") == name
		})
		if i < 0 {
			out.Content = append(out.Content, step)
			continue
		}
		out.Content[i] = mergeNode(withoutProviderSettings(out.Content[i], step), step, append(slices.Clone(path), name))
	}
	return &out
}

// withoutProviderSettings drops base's model and vendor when over switches the step to a different
// provider, so a model name meant for one provider isn't passed to another.
func withoutProviderSettings(base, over *yaml.Node) *yaml.Node {
	provider := scalarValue(over, "provider")
	if provider == "" || provider == scalarValue(base, "provider") {
		return base
	}
	out := *base
	out.Content = nil
	for i := 0; i+1 < len(base.Content); i += 2 {
		if key := base.Content[i].Value; key == "model" || key == "vendor" {
			continue
		}
		out.Content = append(out.Content, base.Content[i], base.Content[i+1])
	}
	return &out
}

// mappingIndex returns the index of key's key node in mapping m, or -1.
func mappingIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// scalarValue returns the scalar value of key in mapping m, or "".
func scalarValue(m *yaml.Node, key string) string {
	if m.Kind != yaml.MappingNode {
		return ""
	}
	i := mappingIndex(m, key)
	if i < 0 || m.Content[i+1].Kind != yaml.ScalarNode {
		return ""
	}
	return m.Content[i+1].Value
}

// applyDefaults fills in values the configuration leaves out.
func applyDefaults(c *Config) {
	for name, s := range c.Sinks {
		// GitHub reports associations in upper case, and that is what the sink compares against, so an
		// operator writing "owner" means the same thing and shouldn't be told it isn't a real value.
		for i, association := range s.Feedback.From {
			s.Feedback.From[i] = strings.ToUpper(strings.TrimSpace(association))
		}
		c.Sinks[name] = s
	}
	for name, p := range c.Providers {
		if p.Adapter == "" {
			p.Adapter = name
		}
		if p.Cmd == "" {
			p.Cmd = p.Adapter
		}
		c.Providers[name] = p
	}
	for _, lc := range c.Lifecycles {
		for i := range lc.Steps {
			if lc.Steps[i].Output == "" {
				lc.Steps[i].Output = roleOutput(lc.Steps[i].Role)
			}
		}
	}
}

// roleOutput returns the output a built-in role produces, or "" for any other role.
func roleOutput(role string) string {
	switch role {
	case "planner":
		return OutputPlan
	case "engineer":
		return OutputChange
	case "qa":
		return OutputVerdict
	default:
		return ""
	}
}
