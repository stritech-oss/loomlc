# Code conventions

These conventions apply to all code in loomlc. The general rules come first; the [Go](#go) section
at the end applies them to Go, loomlc's implementation language
([`PLAN.md` §10](../PLAN.md#10-open-decisions)).

## Design

- **Use the plan's vocabulary.** Name things with the terms in
  [`PLAN.md` §3](../PLAN.md#3-core-concepts-glossary): Task, Source, Lifecycle, Step, Provider,
  Executor, Sink, Gate, Run. Don't introduce synonyms such as "job", "pipeline", or "stage" for the
  same concepts.
- **Keep the engine and adapters apart.** The engine depends on the Provider, Source, Sink, and
  Executor interfaces, never on a concrete adapter. Adapters don't import each other. Only the wiring
  that builds adapters from config knows the concrete types.
- **Keep interfaces small.** Define an interface where it's used, with only the methods that caller
  needs.
- **Pass dependencies in.** Take clocks, process runners, and filesystem and network clients as
  parameters instead of reaching for globals, so behavior is deterministic and testable.
- **Build for the current phase.** Add an abstraction when a second real use arrives, not in
  anticipation of one.

## Functions and naming

- A function does one thing at one level of abstraction. If describing it needs "and", split it.
- Handle errors and edge cases first and return early, so the main path isn't nested.
- Don't use boolean parameters to switch behavior. Write two functions or take an options value.
- Name for intent at the call site: `claimTask`, not `process`; `openOutputs`, not `data`.
- Declare values close to where they're used.

## Errors

- Never ignore an error. Handle it, or return it with context about what was being attempted, as in
  `claim task gh#42: …`.
- Fail fast on invalid config and broken invariants, with a message that says how to fix the problem.
- Crash (panic, uncaught exception) only for programmer errors, never for expected failures such as a
  provider CLI exiting non-zero.

## Security

loomlc passes untrusted task text to agents that hold real credentials
([`PLAN.md` §6](../PLAN.md#6-security-model)). These rules aren't optional:

- **No shell strings.** Run subprocesses with an argument vector. Never build a shell command from
  task text, config values, or provider output.
- **Never log or echo secrets.** Redact tokens and keys in logs, errors, run state, and anything sent
  to a sink.
- **Treat task text and provider output as untrusted input.** Validate and bound it before acting on
  it.
- **Default to deny.** New capabilities, such as network access, mounts, and permissions, start
  disabled and are turned on explicitly.

## Comments and dead code

- Comments explain *why*: constraints, trade-offs, links to decisions. The code already says *what*.
- Keep them short: one line where one line does, two when the *why* needs it. An explanation that runs
  longer belongs in the commit message or the issue, with a pointer left behind.
- Delete dead and commented-out code; git keeps the history.
- Remove debug output before committing.
- Every `TODO` names an issue: `TODO(#42): resume after an executor restart`.

## Tests

- Every behavior change comes with tests. Every bug fix starts with a failing test that reproduces
  the bug.
- Unit tests never call real provider CLIs, model APIs, forges, or the network. Use fakes behind the
  interfaces above.
- Test provider output parsing against recorded fixtures (golden files), so a change in a CLI's
  output format shows up as a test diff.
- Keep tests deterministic: no sleeps, real clocks, or dependence on test order.
- Name tests after the behavior they check, not the function they call.

## Formatting and linting

Formatting is automated, never argued in review. `gofmt` and `goimports` format the code, and
golangci-lint (configured in `.golangci.yml`) lints it. Both are required CI checks; run
`task check` before pushing.

## Go

These apply the rules above to Go. Where they differ from general Go advice, these win.

- **Layout.** The binary lives in `cmd/loomlc`. Everything else goes under `internal/`, so nothing is
  importable from outside the module. Name packages after what they provide (`config`, `worktree`),
  never `util` or `common`.
- **Contexts.** A function that does I/O, runs a subprocess, or can block takes a `context.Context` as
  its first parameter and honors cancellation.
- **Errors.** Wrap with `%w` and the action being attempted:
  `fmt.Errorf("claim task %s: %w", ref, err)`. Check errors with `errors.Is` and `errors.As`, never by
  comparing strings. Return errors; don't log them and carry on.
- **Interfaces.** Declare them in the package that consumes them, with only the methods it calls.
  Constructors return concrete types.
- **No hidden state.** No mutable package-level variables and no `init` functions. Pass the clock,
  process runner, environment, and filesystem in. The one exception is `internal/version.Version`,
  which the linker sets.
- **Subprocesses.** Only `internal/proc` imports `os/exec`, and it always uses an argument vector.
  Everything else runs commands through its `Runner` interface.
- **Output.** Commands write to the `io.Writer`s they're given, never to `os.Stdout` or `os.Stderr`
  directly, so they can be tested.
- **Tests.** Table-driven, with `t.Run` names that describe the behavior. Test data lives in
  `testdata/`. Golden files are regenerated with `go test ./internal/<pkg> -update` and reviewed like
  code. Test external commands with fakes or the helper-process pattern, never the real tool. Run
  tests with `-race`.
- **Dependencies.** Prefer the standard library. A new module needs a reason in the pull request
  description, and `go mod tidy` must leave `go.mod` and `go.sum` unchanged.
