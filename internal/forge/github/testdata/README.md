# github forge test data

These fixtures stand in for gh's output in tests. They're written by hand, but every field name and shape
was checked against gh 2.100.0 on 2026-09-17 by running the same queries against real repositories:

| Fixture | Query it stands in for | How the shape was checked |
|---|---|---|
| `issues_ready.json` | `gh issue list --json number,labels` | Run against a repository with labelled issues; labels are objects with `name`. |
| `issue_view.json` | `gh issue view <n> --json number,title,body,url,labels,closed` | Run against a real issue; `gh issue view --json` lists these fields as available. |
| `prs_feedback.json` | `gh pr list --json number,labels,isCrossRepository` | `gh pr view --json` lists `isCrossRepository` among its fields. |
| `pr_view.json` | `gh pr view <n> --json number,url,state,headRefName,baseRefName,isCrossRepository,labels` | Run against a real pull request; `state` comes back uppercase, such as `OPEN`. |
| `pr_conversation.json` | `gh pr view <n> --json reviews,comments` | Comments were read from a real pull request: `id`, `author.login`, `body`, `createdAt`. Reviews were read from a public repository, since neither loomlc nor strive-ui.io had one: `id`, `author.login`, `authorAssociation`, `state`, `submittedAt`, `body`. |
| `pr_inline_comments.json` | `gh api --paginate --slurp repos/<repo>/pulls/<n>/comments` | `--slurp` wraps each page in an outer array, so the fixture is an array of pages. Fields are the REST shape: `id`, `user.login`, `path`, `line`, `created_at`, `body`. |

Two details the fixtures capture on purpose:

- A bot's comment can have a plain login such as `dependabot`, with no `[bot]` suffix, so filtering on the
  suffix alone isn't enough.
- `gh api --slurp` can't be combined with `--jq`, so loomlc parses the pages in Go.
