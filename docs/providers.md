# Providers

A provider runs a lifecycle step with an agent CLI. loomlc never calls a model API itself: each adapter
turns a step into a headless invocation of its CLI and reads the answer back ([`PLAN.md`
§4.2](../PLAN.md#42-provider-adapters-the-portability-core)).

Run `loomlc providers` to see the configured providers, whether their CLIs are installed, and what their
adapters support.

## Capability matrix

| Capability | claude | codex | pi |
|---|---|---|---|
| Adapter available | ✅ | planned (Phase 0) | planned (Phase 0) |
| Structured output (schema enforced by the CLI) | ✅ verified | expected, unverified | ❌ fenced JSON only |
| Resume a session by id | ✅ verified | expected, unverified | expected, unverified |
| Own permission model | ✅ | expected (its sandbox), unverified | ❌ needs a sandbox |
| System prompt for role instructions | ✅ verified | ❌ prepended to the prompt | expected, unverified |
| Cost report | ✅ verified | unverified | unverified |

**Verified** means checked against the installed CLI, with the recorded output kept as test fixtures.
**Unverified** means documented by the vendor but not yet run: codex and pi aren't installed on the
machine Phase 0 is built on. Their adapters will be tested with hand-written fixtures until someone runs
the real CLIs and records them.

## claude

The claude adapter runs Claude Code in headless mode. Verified against Claude Code 2.1.269 on 2026-09-13;
the recorded fixtures and their commands are in
[`internal/provider/claude/testdata/README.md`](../internal/provider/claude/testdata/README.md).

### Invocation

```
claude -p --output-format json
  --allowedTools <rules> --disallowedTools <rules>
  --permission-mode <mode> --permission-prompts none
  [--model <model>] [--append-system-prompt <role prompt>] [--json-schema <schema>] [--resume <session>]
  < task prompt on stdin
```

- **The task prompt goes on stdin.** Task text is untrusted, so it never appears in arguments, where it
  would show up in the process list.
- **Role instructions** are appended to Claude Code's system prompt, which keeps its coding defaults and
  its `CLAUDE.md` loading. `--bare` isn't used, because it only supports API-key authentication.
- **Permissions.** `--permission-prompts none` denies anything that would ask for approval, so the rules
  below are the whole policy:
  - Read-only steps use `--permission-mode dontAsk` and deny `Edit`, `Write`, and `NotebookEdit`.
  - Steps that edit files use the provider's `permission_mode` (`acceptEdits` by default).
  - Every step may run `git log`, `git diff`, `git show`, and `git status`, plus the step's
    `allowed_commands`.
  - Every step is denied `git commit`, `git push`, `git reset`, `git rebase`, `git checkout`,
    `git switch`, and `gh`: loomlc makes commits and forge calls itself. These rules also override a
    repository's own `.claude/settings.json` allowances.
- **Tool rules are one comma-separated argument per flag**, placed before the other flags so claude's
  variadic parsing can't swallow them. An allowed command therefore can't contain a comma or a
  parenthesis.

### Reading the result

claude prints one JSON object. The adapter reads `result`, `session_id`, `total_cost_usd`, `is_error`,
and `structured_output`, and ignores the rest.

| Situation | Outcome |
|---|---|
| `is_error` is false and the exit code is 0 | Success. With a schema, the answer is `structured_output`, or else the last fenced json block in `result`. |
| `is_error` is true, or the exit code isn't 0, with JSON output | `Reported`, with claude's `result` as the message. A failed run can still say `subtype: "success"`. |
| No JSON and a non-zero exit code | `Failed`, with the end of stderr as the message. |
| No JSON and exit code 0, or a schema was requested but no JSON answer came back | `BadOutput`. |
| The step's timeout passed, or the run was canceled | `Timeout` or `Canceled`. claude gets SIGTERM, then SIGKILL after 15 seconds. |
| The executable isn't on `PATH` | `NotInstalled`. |

### Re-verifying a new claude release

```
go test -tags live ./internal/provider/claude -run TestLive -v
```

This runs the adapter against the installed claude, with the `haiku` model, in three steps:

1. A read-only step that reads a file and answers through a schema.
2. A step that resumes that session.
3. A step that edits a file.

It uses your Claude subscription or API key, so the `live` build tag keeps it out of `task check` and CI.
When it passes on a new release, re-record the fixtures as described in
[`internal/provider/claude/testdata/README.md`](../internal/provider/claude/testdata/README.md) and update
the version above.
