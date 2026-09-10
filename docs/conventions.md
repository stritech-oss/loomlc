# Code conventions

These conventions apply to all code in loomlc. They're language-neutral while the implementation
language is still an open decision ([`PLAN.md` §10](../PLAN.md#10-open-decisions)). When it's decided,
add a section for that language at the end instead of restating these rules.

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

Formatting is automated, never argued in review. When the language is chosen, its standard formatter
and linter become required CI checks, and this section names them.
