# Contributing to loomlc

This guide is the source of truth for how changes land in loomlc, for people and agents alike. Agents
also read [`AGENTS.md`](../AGENTS.md), which points back here.

## Setup

Enable the repository's git hooks once per clone:

```bash
git config core.hooksPath .githooks
```

The `commit-msg` hook runs [`scripts/commit-policy.sh`](../scripts/commit-policy.sh) on every commit,
so problems show up before you push. CI runs the same checks on every pull request, so skipping the
hook with `--no-verify` only delays the failure.

## Branches

Never commit to or push `main`. Branch from `main` with one of these prefixes, optionally followed by
an issue number:

| Prefix | For | Example |
|---|---|---|
| `feat/` | New functionality | `feat/12-claude-provider` |
| `bug/` | Bug fixes | `bug/resume-loses-session-id` |
| `doc/` | Documentation only | `doc/capability-matrix` |
| `mnt/` | Build, CI, tooling, and repository maintenance | `mnt/commit-policy` |

## Commits

### Format

- Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/): `type(scope): subject`.
  Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`,
  `revert`. Mark breaking changes with `!`, as in `feat(config)!: rename provider field`.
- Keep the header to 72 characters or fewer, in the imperative present tense ("add", not "added"),
  with no trailing period. Leave a blank line before the body.
- Make each commit one logical change. Use the body to explain *why* when the diff doesn't show it.
- Put issue-closing keywords (`Fixes #12`) in the pull request description, not in commits.
- Squash `fixup!` and `squash!` commits before merging. The hook allows them locally; CI rejects them.

### Sign-off

Every commit needs a `Signed-off-by` trailer. Add it with `git commit -s`, which uses your configured
git identity.

- **The author signs off.** The sign-off's email must match the commit author's email.
- **A human signs off.** A sign-off from a bot or agent identity doesn't count on its own. When a bot
  or agent authors a commit, a human who has reviewed the change adds their own sign-off before it
  goes up for review.
- **Only sign off as yourself.** Never type a `Signed-off-by` line by hand or add one for someone else.
  A sign-off states that the signer has the right to submit the change and takes responsibility for it.

Bot and agent identities are recognized by a `[bot]` suffix or a known agent email, never by name
alone. The list is `bot_ident_re` in `scripts/commit-policy.sh`.

### Signing

Sign your commits with a GPG or SSH key that's registered to your GitHub account and matches the
commit author's email:

```bash
git config user.signingkey <key-id>
git config commit.gpgsign true
```

Check a commit with `git log --show-signature -1`. Don't use `--no-gpg-sign`.

Signatures don't survive merging. Pull requests land through GitHub's rebase merge, which rewrites
each commit without a signature, so commits on `main` are unsigned. That's accepted: the sign-off is
the rule that records responsibility, it's kept through the merge, and CI enforces it. Branch
protection doesn't require signed commits, because that would block rebase merges.

### No agent attribution

Commits and pull request descriptions must not contain:

- `Co-authored-by` trailers, for agents or people. Someone who shares responsibility for a change
  adds their own `Signed-off-by` instead.
- Agent session trailers, such as `<Agent>-Session: …`, or links to agent sessions, tasks, or chats.
- "Generated with" lines or footers naming an agent.

Responsibility for a change rests with the humans who sign off, whatever tools helped write it. If
your tool adds these lines by default, turn that off in its settings. For Claude Code, this
repository's `.claude/settings.json` already does. The banned patterns are `banned_res` in
`scripts/commit-policy.sh`.

### Check your commits

```bash
scripts/commit-policy.sh range origin/main HEAD   # every commit on your branch
scripts/commit-policy.test.sh                     # the checker's own tests
```

## Pull requests

- Fill in [the template](../.github/pull_request_template.md): what changed and why, how you
  verified it, and any drive-by changes.
- Keep pull requests small and focused. Keep drive-by fixes small and list them; anything bigger gets
  its own pull request. Don't mix refactors with behavior changes.
- Justify any new dependency in the description: what it's for, why existing code or the standard
  library isn't enough, and its license and maintenance status.
- A human reviews and merges every pull request. Agents never merge, force-push shared branches, or
  push to `main`.
- Changes to agent rules, the commit policy, or CI need a code owner's review (see
  [`.github/CODEOWNERS`](../.github/CODEOWNERS)).
