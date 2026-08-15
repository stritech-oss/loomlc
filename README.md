# loom

> **Working name — rename freely.** The plan is what matters, not the name.

**loom** is a provider-agnostic orchestrator that runs **tasks** through a configurable **lifecycle of
agent steps**, where each step can be executed by a *different* AI provider's CLI — Anthropic
`claude`, Google `gemini`, OpenAI `codex`, etc. — over pluggable **task sources** (GitHub, GitLab,
Jira, Linear, local files) and **outputs** (PRs, MRs, comments, files).

It is **not a model harness.** It does not call model APIs or reimplement agent loops. It installs and
**shells out to each provider's own CLI**, and orchestrates them: pickup → run steps → verify → emit
output, with isolation, scoped credentials, backpressure, and resume.

## Origin

loom generalizes the SDLC agent flow built in the sibling repo `strive-ui.io`
(`plan → engineer ↔ QA → PR`, driven by a local runner + `.claude/**` agents/commands). That flow
proved the pattern; loom lifts it into a **provider-portable, source-agnostic, generic task-lifecycle
engine** — the engineering (SDLC) flow becomes just one shipped preset.

## Status

**Design / planning.** Nothing is implemented yet. See **[PLAN.md](./PLAN.md)** for the full plan and
**[docs/examples/loom.yml](./docs/examples/loom.yml)** for a sample config.
