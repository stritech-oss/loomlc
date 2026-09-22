package cli

import (
	"errors"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stritech-oss/loomlc/internal/config"
	"github.com/stritech-oss/loomlc/internal/provider"
	"github.com/stritech-oss/loomlc/internal/provider/claude"
)

func runProviders(args []string, env Env) int {
	flags := flag.NewFlagSet("loomlc providers", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	configPath := flags.String("config", "", "read the configuration from `path` instead of ./"+defaultConfigFile)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return printUsage(env.Stdout, flags)
		}
		return ExitUsage
	}
	if flags.NArg() > 0 {
		return fail(env.Stderr, fmt.Sprintf("loomlc providers: unexpected arguments %q\n", flags.Args()))
	}
	if err := checkConfigFlag(flags); err != nil {
		return fail(env.Stderr, fmt.Sprintf("loomlc providers: %v\n", err))
	}

	cfg, origin, err := loadConfig(env, *configPath)
	if err != nil {
		return failure(env.Stderr, fmt.Sprintf("loomlc providers: %v\n", err))
	}
	return print(env.Stdout, describeProviders(cfg, origin, env.LookPath))
}

// adapterCapabilities returns the capabilities of the named adapter, and false for adapters loomlc
// doesn't have yet.
func adapterCapabilities(adapter string) (provider.Capabilities, bool) {
	switch adapter {
	case config.AdapterClaude:
		return claude.Capabilities(), true
	default:
		return provider.Capabilities{}, false
	}
}

// describeProviders renders each configured provider with whether its CLI is installed and what its
// adapter supports.
func describeProviders(cfg *config.Config, origin string, lookPath func(string) (string, error)) string {
	var b strings.Builder
	b.WriteString("Configuration: " + origin + "\n\n")

	rows := [][]string{{"PROVIDER", "ADAPTER", "COMMAND", "INSTALLED", "CAPABILITIES"}}
	for _, name := range slices.Sorted(maps.Keys(cfg.Providers)) {
		p := cfg.Providers[name]
		installed := "not found"
		if path, err := lookPath(p.Cmd); err == nil {
			installed = path
		}
		rows = append(rows, []string{name, p.Adapter, p.Cmd, installed, capabilityLabel(p.Adapter)})
	}
	writeTable(&b, "", rows)
	return b.String()
}

func capabilityLabel(adapter string) string {
	caps, ok := adapterCapabilities(adapter)
	if !ok {
		return "adapter not available yet"
	}
	var labels []string
	for _, c := range []struct {
		has   bool
		label string
	}{
		{caps.StructuredOutput, "structured output"},
		{caps.Resume, "resume"},
		{caps.PermissionModel, "own permissions"},
		{caps.RequiresSandbox, "needs a sandbox"},
		{caps.SystemPrompt, "system prompt"},
		{caps.CostReport, "cost report"},
	} {
		if c.has {
			labels = append(labels, c.label)
		}
	}
	return strings.Join(labels, ", ")
}
