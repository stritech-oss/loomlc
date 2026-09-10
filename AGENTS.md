# AGENTS.md — loomlc

Knowledge base for AI agents working in this repository, whichever agent you are. Read this first,
then the source-of-truth docs it points to. `CLAUDE.md` imports this file, so Claude Code reads the
same rules.

## What this project is

**loomlc** (*loom lifecycle*) is a provider-agnostic orchestrator. It takes **tasks** from pluggable
sources (GitHub, GitLab, Jira, Linear, files) and runs them through configurable **lifecycles** of
agent steps. Each step can use a different AI provider's CLI (`claude`, `codex`, `pi`, and others),
inside an isolated executor, and results go to pluggable sinks (PRs, MRs, comments, files).

It's an **orchestrator, not a harness**: it never calls model APIs itself.

## Status

**Design phase. Nothing is implemented yet.** The implementation language (Go is recommended),
license, and config surface are open decisions ([`PLAN.md` §10](PLAN.md#10-open-decisions)). Don't
scaffold code, choose a language, or add a build system unless the task asks for it.

## Source-of-truth docs (read, don't restate)

- **`PLAN.md`**: architecture, glossary, security model, roadmap. **Authoritative for design.**
- **`docs/contributing.md`**: branches, commit format, sign-off, signing, attribution, pull requests.
  **Authoritative for process.**
- **`docs/conventions.md`**: design, errors, security, and testing rules. **Authoritative for code.**
- **`docs/examples/loomlc.yml`**: the provisional config schema.

If your change contradicts one of these, update the doc in the same pull request, or stop and ask.

## Where things live

```
PLAN.md                    design plan
docs/contributing.md       process rules
docs/conventions.md        code rules
docs/examples/loomlc.yml   example config
scripts/commit-policy.sh   commit and PR description checker (hook and CI)
.githooks/commit-msg       local hook that runs the checker
.github/                   PR template, CODEOWNERS, CI workflows
.claude/settings.json      Claude Code project settings
```

## Commit rules

These are enforced by the `commit-msg` hook and by CI. Details are in `docs/contributing.md`.

- **No agent attribution.** No `Co-authored-by` trailers, no agent session trailers or links (Claude,
  Codex, Gemini, Copilot, Cursor, or any other agent), and no "Generated with" lines, in commits or
  PR descriptions. If your tool adds these by default, turn that off in its settings.
- **The author signs off.** Commit with `git commit -s` under the git identity you were given. Never
  type a `Signed-off-by` line by hand or add one for someone else.
- **A human signs off.** Bot- and agent-only sign-offs are rejected. If you commit under an agent
  identity, a human adds their sign-off after reviewing the change.
- **Conventional Commits**: header of 72 characters or fewer, imperative, no trailing period, one
  logical change per commit.

Also sign commits locally with the author's key, and don't pass `--no-gpg-sign`. Signing isn't
enforced: GitHub's rebase merge drops signatures, so commits on `main` are unsigned. The sign-off is
the rule that counts.

Before pushing, run `scripts/commit-policy.sh range origin/main HEAD`.

## Git and pull requests

- Never commit to or push `main`. Branch with `feat/`, `bug/`, `doc/`, or `mnt/`.
- Never merge pull requests, force-push shared branches, rewrite published history, or skip hooks
  with `--no-verify`. A human reviews and merges.
- Fill in `.github/pull_request_template.md`. Issue-closing keywords go in the PR description, not in
  commits.

## Guardrails

- **Don't change your own rules unasked.** Edit `AGENTS.md`, `CLAUDE.md`, `.claude/`, `.githooks/`,
  `.github/`, `scripts/commit-policy*`, `docs/contributing.md`, or `docs/conventions.md` only when the
  task explicitly asks. These paths are code-owned.
- **No secrets.** Never commit, log, or paste tokens, keys, or credentials, including in PR
  descriptions and test fixtures.
- **Verify before you assert.** Check external tool behavior (CLI flags, config keys, API shapes)
  against current docs or by running the tool, and cite the source. Label anything unverified, as
  `PLAN.md` does.
- **Justify dependencies.** Don't add one without explaining why in the PR description.
- **Stay in scope.** Keep drive-by fixes small and list them in the PR description. Anything bigger
  gets its own pull request.

## Code

Follow `docs/conventions.md`. Highlights:

- Name things with the `PLAN.md` §3 glossary; don't invent synonyms.
- Run subprocesses with argument vectors, never shell strings built from task text.
- Unit tests never call real provider CLIs, model APIs, or the network.
- No dead code, commented-out code, debug output, or `TODO`s without an issue number.

## Known gaps

- There are no build or test commands yet, apart from `scripts/commit-policy.test.sh`. A commands
  table goes here once the language is chosen.
- `docs/conventions.md` has no language-specific section yet.
- Code-owner review and the Commit policy check only become required once branch protection is
  enabled on GitHub. Branch protection doesn't require signed commits, because that would block
  rebase merges.
