# Configuration

loomlc reads its configuration from `loomlc.yml`. This page describes the schema Phase 0 supports.
[`docs/examples/loomlc.yml`](examples/loomlc.yml) sketches where the schema is headed, including
executors and sources that don't exist yet.

Run `loomlc lifecycles` to see the configuration loomlc resolves. `loomlc lifecycles --config <path>`
checks a file somewhere other than `./loomlc.yml`.

## How loomlc finds and merges configuration

The built-in `sdlc` preset, [`internal/config/preset/sdlc.yml`](../internal/config/preset/sdlc.yml), is
always the base. loomlc merges one file over it:

1. The file given with `--config`, which must exist.
2. Otherwise `loomlc.yml` in the current directory, if there is one.
3. Otherwise nothing: the preset is used on its own.

Because your file is merged over the preset, it only needs the settings you want to change:

- **Mappings merge by key.** Anything you leave out keeps the preset's value.
- **Lists replace.** `protected_paths: [docs]` replaces the preset's list instead of adding to it.
- **A lifecycle's steps merge by name.** A step with a name the preset already has changes only the
  fields you give. A new name adds a step.
- **Changing a step's provider clears its model and vendor**, so a model meant for one provider is never
  passed to another. Give `model`, and `vendor` for pi, together with the new provider.

Unknown keys are errors, reported with their line number. After merging, loomlc checks the whole
configuration and reports every problem at once, each with its path, such as
`lifecycles.sdlc.steps[2].timeout`.

### Example: a different provider for each role

```yaml
lifecycles:
  sdlc:
    steps:
      - name: plan
        provider: pi
        vendor: google
        model: gemini-3-pro
      - name: qa
        provider: codex
        model: gpt-5-codex
        gate: ["go vet ./...", [go, test, -race, ./...]]
```

The engineer step keeps the preset's claude settings.

## Value formats

- **Durations** are Go duration strings, such as `30m` or `1h30m`.
- **Commands**, in `gate`, `allowed_commands`, and `publish`, are argument lists such as
  `[go, test, ./...]`. A plain string is split on whitespace instead, but only if it has no shell
  syntax: none of `|&;<>()$"'*?~#`, a backtick, or a backslash. loomlc never runs a shell.
- **Names** of providers, executors, sources, sinks, lifecycles, and steps start with a lowercase letter
  and use only lowercase letters, digits, and hyphens.
- **Paths** are relative to the repository and can't leave it.

## Reference

### `version`

Must be `1`.

### `providers.<name>`

The preset defines `claude`, `codex`, and `pi`.

| Key | Default | Meaning |
|---|---|---|
| `adapter` | the provider's name | `claude`, `codex`, or `pi`. |
| `cmd` | the adapter's name | The executable, as a name on `PATH` or a path. No arguments. |
| `permission_mode` | `acceptEdits` for claude | claude only. The permission mode for steps that edit files: `acceptEdits`, `auto`, `bypassPermissions`, `dontAsk`, `manual`, or `plan`. |

### `executors.<name>`

The preset defines `local`.

| Key | Default | Meaning |
|---|---|---|
| `type` | `worktree` | Phase 0 has only `worktree`. `docker` and `workshop` arrive in Phase 5. |
| `root` | `.loomlc/worktrees` | Directory that holds workspaces. |
| `remote` | `origin` | Git remote that workspaces fetch from and push to. |

A `worktree` workspace is a git worktree of your checkout, in `<root>/<task>`, with the task's branch
started from the remote's latest base branch. It keeps runs out of each other's files, but it isn't a
security boundary: a run has your user account's full access. Before a run resets a branch that has
local commits the remote doesn't, loomlc saves them under `refs/loomlc/attempts/<branch>/<run>`, so an
earlier attempt's work is never lost. When a run ends, its workspace is removed, including uncommitted
changes; the branch and its commits stay.

### `sources.<name>`

The preset defines `github`.

| Key | Default | Meaning |
|---|---|---|
| `type` | `github` | Phase 0 has only `github`. |
| `repo` | the checkout's repository | GitHub repository, written as `owner/name`. |
| `trigger.label` | `agent-ready` | Label that marks a task as ready to pick up. |
| `labels.in_progress` | `agent-in-progress` | Label on a task while a run works on it. |
| `labels.done` | `agent-done` | Label on a task whose run opened a pull request. |
| `labels.failed` | `agent-failed` | Label on a task whose run didn't. |

All four labels must be different.

### `sinks.<name>`

The preset defines `github-pr`.

| Key | Default | Meaning |
|---|---|---|
| `type` | `github-pr` | Phase 0 has only `github-pr`. |
| `base` | `main` | Branch that pull requests merge into. |
| `label` | `agent-pr` | Label on pull requests loomlc opens. |
| `reviewer` | none | GitHub username to request a review from. |
| `draft` | `false` | Open pull requests as drafts. |
| `template` | `.github/pull_request_template.md` | Pull request template. |
| `template_sections.description`, `.qa`, `.issue` | see the preset | Template headings loomlc fills in, matched case-insensitively. |
| `feedback.label` | `agent-revise` | Label a reviewer adds to have loomlc act on review feedback. |
| `feedback.in_progress` | `agent-revise-in-progress` | Label loomlc adds while it works on that feedback. |
| `feedback.since_last_reply` | `true` | Only use feedback posted after loomlc's last reply. |
| `feedback.from` | `[OWNER, MEMBER, COLLABORATOR]` | Whose comments count as feedback, by GitHub author association, in any case. Anyone can comment on a pull request in a public repository, and an agent acts on what it is given, so the default is the repository's owner, the owning organization's members, and invited collaborators. `CONTRIBUTOR` means anyone whose patch was ever merged, and `NONE` means anyone at all — widen deliberately. |

The three labels must be different.

### `lifecycles.<name>`

The preset defines `sdlc`.

| Key | Default | Meaning |
|---|---|---|
| `executor`, `source`, `sink` | `local`, `github`, `github-pr` | Names defined in the sections above. A `github-pr` sink needs a `github` source. |
| `concurrency` | `3` | How many tasks run at once. At least 1. |
| `max_open_outputs` | `5` | New pickups pause while this many pull requests await review. At least 1. |
| `watch_interval` | `5m` | How often `watch` looks for work. At least `30s`. |
| `branch` | `feat/issue-{{.ID}}-{{.Slug}}` | Go template for a task's branch. It must use `{{.ID}}`, render a valid branch name, and never render the base branch. `{{.Slug}}` is the task title in lowercase, with other characters replaced by hyphens. |
| `protected_paths` | `[loomlc.yml, prompts]` | Paths a run can't change without a human. |
| `steps` | plan, engineer, qa | See below. |
| `feedback.steps` | `[engineer, qa]` | The steps a feedback run uses: the change step, then the verdict step it loops with. |
| `publish.gates` | none | Commands that must pass before loomlc pushes. |
| `publish.body_gates` | none | Commands that check the pull request description before it's posted. |

### `lifecycles.<name>.steps[]`

In Phase 0, every lifecycle has exactly three steps, in this order: a plan step, a change step with
`loop_with`, and the verdict step it loops with.

| Key | Preset | Meaning |
|---|---|---|
| `name` | `plan`, `engineer`, `qa` | Unique within the lifecycle. |
| `provider` | `claude` | A provider defined under `providers`. |
| `vendor` | none | pi only, and required for pi: the model vendor, such as `anthropic` or `google`. |
| `model` | `sonnet` | The model. Leave it out to use the provider's default; pi requires it. |
| `role` | `planner`, `engineer`, `qa` | A built-in role, or a path to a `.md` prompt in the repository. |
| `output` | set by the role | `plan`, `change`, or `verdict`. Required for a custom role. |
| `readonly` | `true`, `false`, `true` | Must be `true` for plan and verdict steps and `false` for the change step. |
| `loop_with` | `qa` on engineer | On the change step: the verdict step to repeat with until it passes. |
| `max_iter` | `5` on engineer | With `loop_with`, from 1 to 20. |
| `timeout` | `30m`, `1h`, `30m` | More than 0 and at most `4h`. |
| `gate` | none | Verdict step only: commands loomlc runs before the step. Any failure fails the iteration. |
| `allowed_commands` | none | Commands the agent may run, for providers with a permission model. |

## Roles and prompts

A step's prompt has two parts. The **role** is the step's standing instructions, sent as the agent's
system prompt. The **task prompt** is what loomlc builds for that run: the task's own text, the plan,
any findings or review feedback, the commits already on the branch, the checks loomlc has run, and the
protected paths. Only the role is configurable — loomlc builds the task prompt itself, because the
engine's contract with each step depends on what's in it.

loomlc ships three roles:

| Role | Output | What it does |
|---|---|---|
| `planner` | `plan` | Reads the task and the repository, and plans one pull request's worth of work: the slice to build, the files, the tests, what's deferred, and any blockers. Blocked plans stop the run before anything is built. |
| `engineer` | `change` | Edits files, then describes the commits loomlc should make: a message and paths for new work, or a `fixes` SHA for a fix to a commit already on the branch. It never runs git itself. |
| `qa` | `verdict` | Reads the change and the results of the checks loomlc already ran, and returns `pass` or `fail` with findings. It verifies; it doesn't edit or commit. |

Every step's answer must match a JSON Schema loomlc gives the provider. An answer that doesn't match,
or that a model declines to give, fails the step rather than being guessed at.

### Writing your own role

Point `role` at a `.md` file in your repository and say what it produces:

```yaml
lifecycles:
  sdlc:
    steps:
      - name: qa
        role: prompts/reviewer.md
        output: verdict
```

The path is relative to the repository root and must stay inside it. loomlc reads it from your
checkout, never from the workspace an agent edits, so a run can't rewrite its own instructions — keep
the prompt directory in `protected_paths` as well, so a run can't propose changing it either. A custom
prompt must be under 64 KiB, since providers such as claude pass it as a command-line argument.

Your prompt replaces the role, not the task prompt or the schema. Tell the agent what to judge or
build; loomlc still tells it what the task is and what its answer has to contain.

### Untrusted text

Task descriptions, review comments, and earlier steps' answers are quoted inside an `<untrusted-text>`
block, labelled with where they came from. Any spelling of that tag inside the quoted text is escaped,
so nothing it contains can close the block early and be read as an instruction. An earlier step's
answer is quoted too: it's a model's words about material loomlc doesn't trust, and it reaches the next
step with none of loomlc's authority.

This defense lives in the task prompt, which loomlc builds and a configuration can't replace, so a
custom role can't weaken it. What a custom role can do is fail to reinforce it: the built-in roles tell
an agent to keep working and report an injection attempt rather than obey it or stop, and a replacement
that says nothing leaves that to the model.
