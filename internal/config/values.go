package config

import (
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// shellSyntax is the set of characters that would need a shell to mean anything. loomlc never runs a
// shell, so a command written as a string must not contain them.
const shellSyntax = "|&;<>()$`\\\"'*?~#"

// Duration is a time.Duration written in YAML as a duration string, such as "30m" or "1h".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if n.Kind != yaml.ScalarNode || n.Decode(&s) != nil {
		return fmt.Errorf("line %d: a duration must be a string such as \"30m\"", n.Line)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q; use a value such as \"30m\" or \"1h\"", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

// Command is an argument vector. YAML can give it as a list, such as [go, test, ./...], or as a string
// split on whitespace, such as "go test ./...", as long as the string has no shell syntax.
type Command []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	var argv []string
	switch n.Kind {
	case yaml.SequenceNode:
		if err := n.Decode(&argv); err != nil {
			return fmt.Errorf("line %d: a command list must contain only strings", n.Line)
		}
	case yaml.ScalarNode:
		if strings.ContainsAny(n.Value, shellSyntax) {
			return fmt.Errorf("line %d: command %q contains shell syntax, but loomlc never runs a shell; write it as a list of arguments instead", n.Line, n.Value)
		}
		argv = strings.Fields(n.Value)
	default:
		return fmt.Errorf("line %d: a command must be a string or a list of arguments", n.Line)
	}
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("line %d: a command can't be empty", n.Line)
	}
	*c = argv
	return nil
}
