# testdata

The `.golden` files are the task prompts `Render` builds, one per step and mode. Update them with:

```sh
go test ./internal/prompt/ -update
```

Read the diff before committing it: these are what an agent is told to do, so a change here changes how
every run behaves.

## What was checked against a real CLI

The schemas were run through `claude` 2.1.270 on 2026-09-20, with
`claude -p --output-format json --json-schema <schema> --permission-mode dontAsk --permission-prompts none`
and a short prompt on stdin.

- **A `$schema` keyword makes claude refuse the schema**, before any model runs:
  `--json-schema is not a valid JSON Schema: no schema with key or ref
  "https://json-schema.org/draft/2020-12/schema"`. The schemas here declare no dialect, and
  `TestSchemasOmitTheDialectKeyword` keeps it that way. The keywords they do use — `type`, `enum`,
  `required`, `additionalProperties`, `properties`, `items`, `minLength`, `maxLength`, `minItems`,
  `maxItems`, `minimum`, `pattern` — were all accepted.
- **All three schemas are accepted** once the dialect keyword is gone: `plan`, `change` and `verdict`
  each ran with `is_error: false`.
- **The `change` schema produced a conforming answer**: `status`, `summary`, `commits`, `addressed` and
  `blockers` were all present, arrays included, with `status: "blocked"` chosen correctly for a prompt
  describing work that hadn't happened.
- **claude offers structured output as a tool the model can decline to call.** Given a prompt with
  nothing to plan, it answered in prose asking for details and returned no `structured_output`, naming
  "the StructuredOutput tool" in its reply. So a missing structured answer is ordinary model behavior,
  not only a malfunction: `internal/provider/claude` falls back to a fenced JSON block and otherwise
  fails the step with `BadOutput`, which is the right handling.

Not yet verified: whether `codex` and `pi` accept these schemas. Their adapters arrive in PRs 15 and 16,
and `docs/providers.md` records what's unverified.
