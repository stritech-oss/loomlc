# claude adapter test data

## Recorded fixtures

Recorded on 2026-09-13 from Claude Code 2.1.269 (`claude --version`), with the `haiku` model, in an
empty scratch git repository. Each prompt was piped on stdin. `session_id` and `uuid` were replaced with
fixed values, and home directory paths with `/home/user`; nothing else was changed.

| File | Command | What it shows |
|---|---|---|
| `success_text.json` | `claude -p --output-format json --model haiku --permission-mode dontAsk --permission-prompts none --no-session-persistence`, prompt "Reply with exactly the single word: pong" | A prompt on stdin with no prompt argument works; `result`, `session_id`, and `total_cost_usd` are set; `structured_output` is absent. |
| `success_structured.json` | `claude -p --output-format json --allowedTools "Bash(git log *),Bash(git status *)" --disallowedTools "Edit,Write,NotebookEdit,Bash(git commit *)" --append-system-prompt "…" --json-schema '<schema>' --model haiku --permission-mode dontAsk --permission-prompts none --no-session-persistence`, prompt "Read note.txt and report the magic word." | Comma-joined tool lists placed before other flags don't swallow them (the model, system prompt, and schema all applied), and `structured_output` is returned after three turns of tool use. |
| `reported_error.json`, `reported_error.stderr` | `claude -p --output-format json --model no-such-model-xyz --permission-mode dontAsk --permission-prompts none --no-session-persistence`, prompt "hi" | A failed run exits 1 but still prints JSON, with `is_error: true` while `subtype` stays `"success"`. |

The same session also confirmed that `--resume <session_id>` continues a session: a follow-up recalled a
number from the first run and returned the same `session_id`.

To re-record, run the commands above in an empty git repository with `note.txt` containing "The magic
word is heliotrope.", then apply the same replacements.

## Golden files

`args_*.golden` hold the arguments `buildArgs` produces, one per line. Regenerate them with
`go test ./internal/provider/claude -update` and review the diff.
