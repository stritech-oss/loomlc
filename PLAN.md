# loom — Plan

> Status: **design / planning**. Working name **loom** is provisional. This document is meant to be
> picked up and iterated on later.

## 1. Context & motivation

We built a working SDLC agent flow in the sibling repo `strive-ui.io`: a local runner
(`scripts/agent-runner.sh`) picks up GitHub issues labelled `agent-ready`, and drives a
`plan → engineer ↔ QA → PR` pipeline using Anthropic's `claude` CLI in headless mode, with subagents
and slash-commands defined under `.claude/`. It works — it produced real PRs and even fixed its own CI.

But it is welded to three things it shouldn't be:

1. **One provider** (Anthropic `claude`).
2. **One task source / forge** (GitHub issues → GitHub PRs).
3. **One lifecycle** (software engineering: plan/build/QA).

**loom** removes all three couplings. It keeps the good ideas (disposable per-task workspaces,
backpressure/caps, an engineer↔verifier loop, a feedback loop, resume, scoped tokens, container
isolation) and turns them into a small, composable engine.

### Explicit goals (from the brief)

- **Provider-portable.** Not just Claude. Each step can run on a *different* provider's CLI
  (`claude`, `gemini`, `codex`, …). We do **not** build a harness — we install and drive the CLI the
  provider ships.
- **Provider-per-step.** e.g. plan with Gemini, build with Claude, review with GPT.
- **Generic task lifecycle.** Software engineering is one preset; the engine runs *any* sequence of
  agent steps over *any* task (research, triage, docs, data, ops…).
- **Source-agnostic.** Not tied to GitHub. Tasks can come from GitHub/GitLab/Jira/Linear/a local
  backlog; outputs can be PRs/MRs/comments/files/notifications.
- **Isolated & least-privilege.** Each run in an isolated sandbox with a repo-/task-scoped token, not
  the operator's personal credentials.

## 2. Principles

- **Orchestrator, not harness.** loom never calls a model API. It resolves a step to a provider CLI
  invocation, runs it in a workspace, and reads the result. Providers own the agent loop, tools, and
  model calls.
- **Everything is an adapter.** Providers, sources, sinks, and executors are all plugins behind small
  interfaces. Shipping a new provider/forge is writing one adapter, not touching the engine.
- **Config over code.** A repo/operator describes lifecycles, provider bindings, source, sink, and
  executor in YAML. The portable engine + prompts are shared; only the config is per-repo.
- **Deny by default.** Isolation and scoped credentials are the default posture, not an add-on.
- **Human at the gate.** loom proposes (PRs/MRs/drafts); a human reviews and merges. No auto-merge.

## 3. Core concepts (glossary)

| Concept | Meaning |
|---|---|
| **Task** | A unit of work with an id, description, and acceptance criteria (a GitHub issue, a Jira ticket, a row in a backlog file, a CLI arg). |
| **Source** | Adapter that lists/claims/updates tasks (GitHub Issues, GitLab, Jira, Linear, files, stdin). |
| **Lifecycle** | A named pipeline of **steps** run over a task (e.g. `sdlc`, `research`, `triage`). |
| **Step** | One stage of a lifecycle, executed by a **provider**. Has a role prompt, inputs, optional **gate**, and loop policy. |
| **Provider** | Adapter that runs an AI CLI headless (`claude`, `gemini`, `codex`, …) for a step. |
| **Executor** | Adapter that provides the **workspace** a run happens in (local git worktree, Docker container, Canonical Workshop env). |
| **Sink / forge** | Adapter that emits the result (open a PR/MR, comment, write files, notify). |
| **Gate** | Verification a step must pass (shell commands, or a provider verdict) before proceeding. |
| **Run** | One execution of a lifecycle over one task; has persisted state for resume. |

## 4. Architecture

```
                         ┌──────────────────────────────────────────────┐
   task sources          │                 loom engine                  │        sinks / forges
 ┌───────────────┐       │  ┌────────────┐   ┌───────────────────────┐  │      ┌───────────────┐
 │ GitHub issues │──┐    │  │  scheduler │   │  lifecycle runner     │  │   ┌──▶│ GitHub PR     │
 │ GitLab / Jira │  ├───▶│  │ pickup+cap │──▶│  step → step → …       │  │───┤   │ GitLab MR     │
 │ Linear        │  │    │  │ concurrency│   │  loop / gate / resume  │  │   └──▶│ comment/file  │
 │ files / stdin │──┘    │  └────────────┘   └───────────┬───────────┘  │      │ Slack/webhook │
 └───────────────┘       │                               │              │      └───────────────┘
                         │         each step runs via ▼  │              │
                         │   ┌───────────────────────────┴───────────┐  │
                         │   │ provider adapter   (per-step binding)  │  │
                         │   │  claude │ gemini │ codex │ exec(...)   │  │
                         │   └───────────────────┬───────────────────┘  │
                         │        inside a ▼ workspace from an executor  │
                         │   ┌───────────────────┴───────────────────┐  │
                         │   │ executor: worktree │ docker │ workshop │  │
                         │   │  + scoped token + egress allowlist     │  │
                         │   └────────────────────────────────────────┘  │
                         └──────────────────────────────────────────────┘
```

### 4.1 Engine

- **Scheduler:** polls the configured source, claims tasks (idempotent status/label transitions),
  enforces concurrency (`AGENT_CONCURRENCY`-style) and **backpressure** (cap on open outputs — the
  generalized "max 5 open PRs"). Modes: one-shot `run <task>`, drain `run --all`, `watch`.
- **Lifecycle runner:** executes steps in order (later: DAG). Supports **loops** between two steps
  (engineer ↔ qa) bounded by `max_iter`, and **gates** (a step only passes if its checks pass).
- **State & resume:** each run persists phase, workspace ref, step outputs, and provider **session
  ids** to a run store, so a blocked/failed/interrupted run can be resumed instead of restarted
  (generalizes `strive-ui.io#66`). Resume preserves committed work; it does **not** bypass isolation
  or permission boundaries.

### 4.2 Provider adapters (the portability core)

A provider adapter maps a normalized step request to a specific CLI invocation and normalizes the
result. Interface (sketch):

```
Provider.run(step, ctx) -> Result
  step: { role_prompt, inputs, allowed_tools/permissions, model, workspace, timeout }
  Result: { text, structured?, artifacts?, session_id?, exit_code }
```

**The hard part — a capability contract.** CLIs differ in: headless invocation, permission/tool
models, subagent support, session/resume, and structured output. Each adapter **declares
capabilities**; the engine targets a normalized contract and **degrades gracefully** (e.g. if a
provider has no native subagents, the engine runs the step as a single agent; if no structured
output, it parses a fenced block). A **capability matrix** in the docs tracks what each adapter
supports. This layer is where most of the real work lives — which is precisely why an **aggregator**
adapter (family (b) below) is valuable: it amortizes that work across many vendors at once.

Adapters fall into **three families**, and the distinction matters more than the individual entries:

**(a) Vendor CLIs** — the vendor's own agent, driven headless. Subscription auth the user already
has, plus vendor-specific behaviour (subagents, skills, slash commands, plugins). One adapter per
vendor, each with its own capability quirks.
- **claude** — Anthropic. Wraps `claude -p "<prompt>" --permission-mode … --model …`
  (`--output-format json` for `session_id` + structured output). Reuses the strive-ui invocation.
  **The Phase 0 adapter** — the strive-ui port depends on Claude subagents and slash commands.
- **codex** — OpenAI Codex CLI.
- **antigravity** — Google. Note: Gemini CLI's individual free tier ends 2026-06-18 and it
  transitions to **Antigravity CLI**; target that, not `gemini`.
- Further candidates as demand appears: Amp, GitHub Copilot CLI, Amazon Q Developer CLI, Qwen Code,
  Cursor CLI.

**(b) Multi-provider harnesses ("aggregators")** — a single CLI that itself speaks to many model
vendors over their HTTP APIs. One adapter buys N vendors behind *one* capability contract.
- **pi** — see §4.2.1. The recommended first aggregator.
- Alternatives occupying the same slot: **OpenCode**, **Goose**, **Aider**, **Crush**, Continue's
  `cn`, Codewhale. Any of these could be a second aggregator adapter; none needs to be, since they
  overlap heavily with each other in what they unlock.

**(c) Escape hatch**
- **exec** — run any command matching a small stdin/stdout+exit-code contract, so a runner without a
  first-class adapter can still be wired in via config.

#### 4.2.1 The `pi` adapter

[**Pi**](https://pi.dev) (MIT, Earendil Inc.) is a minimal agent harness: four built-in tools, a tiny
system prompt, and a TypeScript extension system for everything else. It reaches **20+ model
vendors** — Anthropic, OpenAI, Azure, Google Gemini/Vertex, xAI, DeepSeek, Bedrock, Mistral, Groq,
OpenRouter, Hugging Face, local llama.cpp — via its own unified client (`@earendil-works/pi-ai`),
**calling provider HTTP APIs directly**. It does *not* shell out to `claude`/`codex`/`gemini`; it
replaces them. Selection is `--provider <vendor> --model <id>`, switchable mid-session.

Why it earns a place here:
- **It collapses the capability-matrix problem.** One adapter, one set of headless/session/structured-output
  semantics, N vendors — instead of N adapters each with their own. This is the single largest
  reduction available to the hardest part of the engine.
- **It is fewer moving parts, not more.** For a multi-vendor setup it replaces three CLI installs and
  three auth conventions with one install and one credential-resolution order.
- **It does not violate §2's "orchestrator, not harness".** Pi owns the agent loop and the model
  calls; loom still resolves a step to a subprocess and reads the result.

What it does **not** replace:
- The **`claude` adapter**. A Claude-Max-only team already has `claude` installed and authenticated;
  pi asks them to install a second tool, re-auth, and accept a generic agent loop in place of the
  subagents and slash-commands Phase 0 is built on.
- **loom.** Pi has no task sources, scheduler, backpressure, cross-run resume, or sinks. Zero overlap
  with the engine.
- **The language choice (§7).** Pi is TypeScript/npm. Drive it as a subprocess like any other CLI; do
  **not** embed its SDK, or Node lands inside loom's single-binary distribution story.

Auth and credentials (feeds §4.4): OAuth `/login` for Claude Pro/Max, ChatGPT Plus/Pro, GitHub
Copilot and xAI, otherwise API keys. Resolution order is `--api-key` flag → `~/.pi/agent/auth.json`
→ environment variable → custom-provider keys. Tokens live in `~/.pi/agent/auth.json` (0600) and
auto-refresh.

Caveats:
- **No permission system.** Pi's own docs: it "does not include a built-in permission system for
  restricting filesystem, process, network, or credential access" — it inherits the launching
  process's permissions. See the executor constraint in §4.4.
- **Churn.** MIT, but young and fast-moving (launched 2025-08, 60k+ stars by mid-2026). Pin a version
  and treat the JSON event schema as unstable.
- **Verify before Phase 3:** that headless mode exposes a **session id + resume** and a **structured
  final output**. §4.1 resume depends on both. The docs show JSONL sessions, a `SessionManager` API,
  and a JSON event stream mode, so this is likely — but it is unconfirmed.

### 4.3 Sources & sinks

- **Sources:** `github`, `gitlab`, `jira`, `linear`, `file` (a YAML/markdown backlog), `stdin`.
  Interface: `list/claim/release/comment/transition`. Trigger is configurable (label, status,
  assignee, or explicit ref).
- **Sinks:** `github-pr`, `gitlab-mr`, `git-branch` (push a branch + open nothing), `file` (write
  outputs), `notify` (Slack/webhook). Interface: `propose(change)/comment/notify`.
- Source and sink are independent: e.g. read tasks from Jira, open PRs on GitHub.

### 4.4 Executors & isolation

The executor gives a step a **workspace** and enforces the isolation posture. Three targets:

| Executor | Isolation | Notes |
|---|---|---|
| **worktree** | none (a git worktree) | Fastest; convenience only. *Not a security boundary.* Good for trusted local use. |
| **docker** | strong, cross-platform | Ephemeral container per run: non-root, read-only rootfs, dropped caps, resource limits, **egress allowlist** (npm/registry, forge host, provider API host only). The portable "real isolation" option. |
| **workshop** | strong, Ubuntu/LXD | **Canonical Workshop** — see §4.5. Best fit on Ubuntu; purpose-built for agent sandboxing. |

Cross-cutting for the isolated executors:
- **Scoped credentials injected per run:** a GitHub **App installation token** (repo-scoped, ~1h,
  `contents`/`pull_requests`/`issues` write) or a fine-grained PAT — never the operator's personal
  `gh` login. Provider auth (`CLAUDE_CODE_OAUTH_TOKEN` / API key) passed as a per-run secret.
- **Egress allowlist** is the mitigation for the `yarn add`-style supply-chain surface: an agent can
  install deps from the registry but can't reach arbitrary hosts.
- **Defense in depth:** where a provider CLI supports its own permission model (e.g. Claude's
  allow/deny), loom passes a scoped policy through — but the sandbox is the real boundary.
- **Adapters with no permission model must be sandboxed.** `pi` (and most aggregators) ship no
  permission layer at all — they inherit the launching process's access. For those adapters the
  sandbox is the *only* boundary, so loom **refuses to run them under the `worktree` executor** and
  requires `docker`/`workshop`. This is an enforced rule, not a recommendation.
- **Per-run credential isolation for file-based auth stores.** `pi` resolves `~/.pi/agent/auth.json`
  **ahead of** environment variables. A stale auth file in the sandbox — or a mounted host `~/.pi` —
  would silently override loom's injected per-run scoped credential and leak the operator's personal
  subscription into the run, contradicting the rule above. Mitigation: set a per-run `HOME` and pass
  credentials explicitly (`--api-key`); never rely on env alone for `pi` steps, and never mount host
  dotfile directories.

### 4.5 Canonical Workshop as an executor (candidate)

[Canonical **Workshop**](https://canonical.com/blog/introducing-workshop-sandboxed-development-environments)
(released 2026-05-27, v0.9.x, open source) launches **sandboxed development environments from a single
YAML file**, built on **unprivileged LXD system containers**, and is *explicitly positioned for
agentic AI* — "code-executing agents operating alongside human developers with tighter access
controls." That is almost exactly loom's isolated-executor requirement, so it's a strong candidate for
the `workshop` executor on Ubuntu/Linux.

Why it fits:
- **snapd-style interface system** for access control — granular, per-resource grants for network
  services, mounts, devices, SSH-agent, display — with **non-privileged defaults**. This maps cleanly
  onto loom's "scoped permissions + egress allowlist" model, at the OS layer rather than hand-rolled.
- **YAML-defined, versionable, reproducible** environments compose naturally with loom's config; a
  loom `workshop` executor can generate/point at a Workshop env spec.
- **SDKs** (Go, Ollama, OpenCode, CUDA, ROCm, custom, via a versioned SDK Store) make it easy to
  provision the toolchain a repo's gates need.

Caveats to validate before committing (from the docs at
`documentation.ubuntu.com/canonical-workshop/stable/`):
- **Non-interactive `exec`**: loom needs to run a command *inside* the env non-interactively and
  capture output. The launch announcement doesn't confirm an `exec`-style subcommand — **verify**.
- **Programmatic network egress control** granularity (allow specific hosts) via the interface system.
- **Platform:** Ubuntu + LXD 6.8+ only (`sudo snap install --classic workshop`). So **`docker` stays
  the cross-platform executor**; `workshop` is the best-in-class Linux option, not a replacement.

Decision: ship `docker` first (portable), add `workshop` as a Linux-optimized executor once the
`exec`/egress questions are confirmed.

### 4.6 Configuration

A single `loom.yml` (global and/or per-repo) declares providers, executors, sources, sinks, and
lifecycles. See [`docs/examples/loom.yml`](./docs/examples/loom.yml). Highlights:
- **Provider-per-step**: each step names its `provider` + `model`.
- **Presets**: `sdlc`, `research`, `triage`, `docs` ship as built-in lifecycles; a repo can override
  or define its own.
- **Prompts**: role prompts (planner/engineer/qa/researcher…) ship embedded and are overridable per
  repo (`prompts/*.md`).

## 5. CLI surface

```
loom init                 # detect stack/source, scaffold loom.yml + prompts + labels/statuses
loom run <task-ref>       # run one task through its lifecycle (foreground)
loom run --all            # drain all ready tasks (concurrency + backpressure), then exit
loom watch                # poll the source and drain continuously
loom feedback <output>    # act on review feedback for an open output (PR/MR) — the feedback loop
loom resume <task|run>    # resume a blocked/failed/interrupted run, preserving work
loom lifecycles|providers|executors   # introspection
loom doctor               # verify provider CLIs, executor, and credentials are wired up
```

Unattended: a `watch` daemon (systemd user unit / cron), same as strive-ui's setup — but now the work
happens inside the configured executor's sandbox.

## 6. Security model

- **Isolation:** every non-trivial run in an isolated executor (`docker`/`workshop`); `worktree` only
  for trusted local use. The workspace is a fresh clone; nothing from the host is mounted.
- **Least-privilege credentials:** repo/task-scoped, short-lived forge tokens (GitHub App preferred);
  per-run provider secrets. Operator's personal creds never enter the sandbox.
- **Egress allowlist:** only the registry, the forge host, and the provider API host are reachable.
- **No auto-merge / no push to default branch:** loom proposes; humans merge. Deny rules on
  destructive/publishing commands as defense-in-depth.
- **Prompt-injection awareness:** task text (esp. from public sources) is untrusted input; the
  sandbox + egress allowlist + human gate contain the blast radius. Only trusted maintainers should be
  able to mark a task "ready" for pickup.
- **Self-modification boundary:** a run cannot silently rewrite loom's own config/prompts — matching
  the lesson that meta-changes need a human (from `strive-ui.io#62`).

## 7. Distribution / packaging

- **Language:** recommend **Go** — single static binary, excellent for shelling out to CLIs +
  containers, trivial cross-platform distribution. (Alternative: TypeScript compiled with
  `bun build --compile`, closer to provider SDKs but heavier; loom shells out rather than embeds SDKs,
  which favours Go.)
- **Ship:** GoReleaser → GitHub Releases + Homebrew tap + `curl | sh`; a runtime container image on
  GHCR for the `docker` executor; a Workshop SDK/env spec for the `workshop` executor.
- **Complementary:** a Claude Code **plugin** (for teams staying on `claude`) and a published
  **GitHub Action** wrapper (turnkey CI usage with a GitHub App token) can reuse the same engine.
- **License:** open decision — Apache-2.0 or MIT for broad adoption (note: `strive-ui.io` is
  AGPL-3.0; loom is a separate project and need not match).

## 8. Roadmap

- **Phase 0 — Port.** Recreate the strive-ui flow as loom's `sdlc` preset: `claude` provider +
  `github` source/sink + `worktree` executor. Prove parity with `agent-runner.sh`.
- **Phase 1 — Engine.** Config schema; lifecycle runner (order, loops, gates); state store + `resume`;
  scheduler (concurrency + backpressure + `watch`).
- **Phase 2 — Provider contract.** Formalize the provider interface + capability matrix; solidify the
  `claude` adapter (`--output-format json`, session ids).
- **Phase 3 — Second provider.** Add **`pi`** to prove **provider-per-step**; add `exec`. Pi is the
  better Phase-3 target than a second vendor CLI: one adapter proves the contract across 20+ vendors,
  it is one install rather than three, and it forces the §4.4 sandbox-required rule early. Native
  `codex`/`antigravity` adapters become demand-driven additions rather than blockers.
- **Phase 4 — Sources/sinks.** Interfaces + `github`; then a second source (`gitlab` or `linear`) and
  the `file` backlog source to prove source-agnosticism.
- **Phase 5 — Isolation.** `docker` executor (non-root, ro-rootfs, egress allowlist) + GitHub App
  scoped tokens. Then evaluate/add the **`workshop`** executor.
- **Phase 6 — Beyond SDLC + distribution.** Ship `research`/`triage`/`docs` presets; GoReleaser +
  Homebrew + Action wrapper + plugin.

## 9. Non-goals

- Not a model harness / SDK (never calls model APIs directly).
- Not a general workflow engine (Temporal/Airflow); specialized for agent-run steps over tasks.
- Not locked to one provider, one forge, or one lifecycle.
- Not a replacement for human review.

## 10. Open decisions

- **Name** (loom is provisional).
- **Language**: Go (recommended) vs TypeScript/Bun.
- **License**: Apache-2.0 vs MIT.
- **Config surface**: single `loom.yml` vs split global/per-repo; DAG vs linear steps for v1 (linear
  first).
- **`provider` naming collision**: loom's `provider` means *adapter*; pi's `--provider` means *model
  vendor*. A step on `pi` needs both. Options: keep `provider:` + add `vendor:` (least disruptive,
  assumed below), or rename loom's field to `runner:` and free `provider:` for the vendor.
  ```yaml
  - role: engineer
    provider: pi          # loom adapter
    vendor: anthropic     # pi --provider
    model: claude-sonnet-4-5
  ```
- **Which aggregator**: `pi` recommended; OpenCode/Goose/Aider/Crush occupy the same slot. Shipping
  more than one buys little — they overlap in the vendors they unlock.
- **Run/state store**: local files vs SQLite (SQLite once resume/parallelism matter).
- **Workshop integration**: confirm non-interactive `exec` + egress-control granularity before making
  it a first-class executor.

## 11. References

- Reference implementation & lessons: sibling repo `strive-ui.io` — `scripts/agent-runner.sh`,
  `.claude/agents/*`, `.claude/commands/{implement-issue,address-feedback}.md`; and issues
  #62 (meta-issue permission wall), #66 (resume), plus the 5-PR cap / feedback-loop / scoped-token work.
- Pi (aggregator provider adapter):
  - Site — https://pi.dev
  - Source — https://github.com/earendil-works/pi
  - Providers & auth — https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/providers.md
  - Unified LLM API — https://github.com/earendil-works/pi/tree/main/packages/ai
- CLI agent landscape (adapter candidates) —
  https://github.com/bradAGI/awesome-cli-coding-agents
- Canonical Workshop:
  - Announcement — https://canonical.com/blog/introducing-workshop-sandboxed-development-environments
  - Docs — https://documentation.ubuntu.com/canonical-workshop/stable/
  - Coverage — https://9to5linux.com/canonical-launches-ubuntu-workshop-for-sandboxed-development-environments ,
    https://www.theregister.com/software/2026/06/08/canonical-sends-ubuntu-into-the-ai-agent-era/5252373
