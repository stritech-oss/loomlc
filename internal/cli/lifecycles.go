package cli

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/stritech-oss/loomlc/internal/config"
)

// defaultConfigFile is read from the current directory when --config isn't given.
const defaultConfigFile = "loomlc.yml"

func runLifecycles(args []string, env Env) int {
	flags := flag.NewFlagSet("loomlc lifecycles", flag.ContinueOnError)
	flags.SetOutput(env.Stderr)
	configPath := flags.String("config", "", "read the configuration from `path` instead of ./"+defaultConfigFile)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	if flags.NArg() > 0 {
		return fail(env.Stderr, fmt.Sprintf("loomlc lifecycles: unexpected arguments %q\n", flags.Args()))
	}

	cfg, origin, err := loadConfig(env, *configPath)
	if err != nil {
		return fail(env.Stderr, fmt.Sprintf("loomlc lifecycles: %v\n", err))
	}
	return print(env.Stdout, describeLifecycles(cfg, origin))
}

// loadConfig reads the configuration at path, or ./loomlc.yml when path is empty, and describes where it
// came from. A missing ./loomlc.yml means the built-in preset alone; a missing explicit path is an error.
func loadConfig(env Env, path string) (*config.Config, string, error) {
	explicit := path != ""
	if !explicit {
		path = defaultConfigFile
	}

	data, err := env.ReadFile(path)
	switch {
	case err == nil:
		cfg, err := config.Parse(path, data)
		return cfg, path + " merged over " + config.PresetName, err
	case !explicit && errors.Is(err, fs.ErrNotExist):
		cfg, err := config.Parse("", nil)
		return cfg, config.PresetName + " (no " + defaultConfigFile + " found)", err
	default:
		return nil, "", fmt.Errorf("read configuration: %w", err)
	}
}

// describeLifecycles renders the lifecycles in cfg for people to read.
func describeLifecycles(cfg *config.Config, origin string) string {
	var b strings.Builder
	b.WriteString("Configuration: " + origin + "\n")

	for _, name := range slices.Sorted(maps.Keys(cfg.Lifecycles)) {
		lc := cfg.Lifecycles[name]
		executor, source, sink := cfg.Executors[lc.Executor], cfg.Sources[lc.Source], cfg.Sinks[lc.Sink]

		b.WriteString("\n" + name + "\n")
		writeTable(&b, "  ", [][]string{
			{"executor", fmt.Sprintf("%s (%s, workspaces in %s)", lc.Executor, executor.Type, executor.Root)},
			{"source", fmt.Sprintf("%s (%s, picks up label %s)", lc.Source, source.Type, source.Trigger.Label)},
			{"sink", fmt.Sprintf("%s (%s into %s, label %s)", lc.Sink, sink.Type, sink.Base, sink.Label)},
			{"limits", fmt.Sprintf("%d at a time, pause at %d open outputs, watch every %s", lc.Concurrency, lc.MaxOpenOutputs, formatDuration(time.Duration(lc.WatchInterval)))},
			{"branch", lc.Branch},
		})

		rows := [][]string{{"STEP", "PROVIDER", "MODEL", "ROLE", "OUTPUT", "ACCESS", "LOOP", "TIMEOUT"}}
		for _, s := range lc.Steps {
			rows = append(rows, []string{s.Name, providerLabel(cfg, s), modelLabel(s), s.Role, s.Output, accessLabel(s), loopLabel(s), formatDuration(time.Duration(s.Timeout))})
		}
		b.WriteString("\n")
		writeTable(&b, "  ", rows)

		for _, s := range lc.Steps {
			if len(s.Gate) > 0 {
				fmt.Fprintf(&b, "  gates before %s: %s\n", s.Name, joinCommands(s.Gate))
			}
		}
	}
	return b.String()
}

// writeTable writes rows as left-aligned columns two spaces apart, each line starting with indent.
func writeTable(b *strings.Builder, indent string, rows [][]string) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len(cell))
		}
	}
	for _, row := range rows {
		b.WriteString(indent)
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len(cell)+2))
			}
		}
		b.WriteString("\n")
	}
}

func providerLabel(cfg *config.Config, s config.Step) string {
	if adapter := cfg.Providers[s.Provider].Adapter; adapter != s.Provider {
		return fmt.Sprintf("%s (%s)", s.Provider, adapter)
	}
	return s.Provider
}

func modelLabel(s config.Step) string {
	switch {
	case s.Model == "":
		return "default"
	case s.Vendor != "":
		return s.Vendor + "/" + s.Model
	default:
		return s.Model
	}
}

func accessLabel(s config.Step) string {
	if s.Readonly {
		return "read-only"
	}
	return "edits files"
}

func loopLabel(s config.Step) string {
	if s.LoopWith == "" {
		return "-"
	}
	return fmt.Sprintf("with %s, max %d", s.LoopWith, s.MaxIter)
}

func joinCommands(cmds []config.Command) string {
	parts := make([]string, len(cmds))
	for i, c := range cmds {
		parts[i] = strings.Join(c, " ")
	}
	return strings.Join(parts, "; ")
}

// formatDuration prints whole hours and minutes compactly ("1h", "30m") instead of "1h0m0s".
func formatDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}
