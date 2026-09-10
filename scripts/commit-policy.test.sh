#!/usr/bin/env bash
# Tests for commit-policy.sh. Run from anywhere: scripts/commit-policy.test.sh
set -uo pipefail

policy="$(cd "$(dirname "$0")" && pwd)/commit-policy.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

passed=0
failed=0
author_name="Ada Human"
author_email="ada@example.com"
sob="Signed-off-by: Ada Human <ada@example.com>"

record() { # <name> <want> <got> <output> <expected substring>
  if [ "$2" = "$3" ] && { [ -z "$5" ] || grep -Fq -- "$5" <<<"$4"; }; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    printf 'FAIL: %s (wanted %s, got %s)\n%s\n\n' "$1" "$2" "$3" "$4" >&2
  fi
}

# expect <pass|fail> <name> <msg|text> <content with \n escapes> [expected error substring]
expect() {
  local out got
  printf '%b' "$4" >"$work/input"
  out=$(GIT_AUTHOR_NAME=$author_name GIT_AUTHOR_EMAIL=$author_email "$policy" "$3" "$work/input" 2>&1) &&
    got=pass || got=fail
  record "$2" "$1" "$got" "$out" "${5:-}"
}

# expect_range <pass|fail> <name> <base> <head> [expected error substring]
expect_range() {
  local out got
  out=$(cd "$work/repo" && "$policy" range "$3" "$4" 2>&1) && got=pass || got=fail
  record "$2" "$1" "$got" "$out" "${5:-}"
}

# --- commit messages -------------------------------------------------------------------------
expect pass "conventional commit with sign-off" msg "feat(engine): add lifecycle runner\n\nRun steps in order.\n\n$sob\n"
expect pass "breaking change marker" msg "feat(config)!: rename provider field\n\n$sob\n"
expect pass "sign-off email is case-insensitive" msg "docs: fix typo\n\nSigned-off-by: Ada Human <ADA@Example.com>\n"
expect pass "prose mention of a banned trailer" msg "docs(contributing): explain trailer policy\n\nWe reject the Co-authored-by trailer.\n\n$sob\n"
expect pass "local fixup commit" msg "fixup! feat(engine): add lifecycle runner\n"
expect pass "comments and verbose diff are ignored" msg "fix(cli): handle empty task ref\n\n$sob\n# Please enter the commit message\n# ------------------------ >8 ------------------------\n+Co-authored-by: Someone <x@example.com>\n"
expect pass "git revert with sign-off" msg "Revert \"feat(engine): add lifecycle runner\"\n\nThis reverts commit abc123.\n\n$sob\n"

expect fail "missing sign-off" msg "fix(cli): handle empty task ref\n" "missing Signed-off-by"
expect fail "sign-off from someone else" msg "fix(cli): handle empty task ref\n\nSigned-off-by: Bob Other <bob@example.com>\n" "no Signed-off-by from the commit author"
expect fail "co-authored-by trailer" msg "fix(cli): handle empty task ref\n\n$sob\nco-authored-by: Claude <noreply@anthropic.com>\n" "agent attribution"
expect fail "agent session trailer" msg "fix(cli): handle empty task ref\n\nClaude-Session: https://example.com/x\n$sob\n" "agent attribution"
expect fail "agent session link in body" msg "fix(cli): handle empty task ref\n\nSee https://claude.ai/code/session_123\n\n$sob\n" "agent attribution"
expect fail "codex task link" msg "fix(cli): handle empty task ref\n\nhttps://chatgpt.com/codex/tasks/abc\n\n$sob\n" "agent attribution"
expect fail "generated-with line" msg "fix(cli): handle empty task ref\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n\n$sob\n" "agent attribution"
expect fail "not a conventional commit" msg "Add stuff\n\n$sob\n" "not a Conventional Commit"
expect fail "unknown type" msg "feature: add stuff\n\n$sob\n" "not a Conventional Commit"
expect fail "header too long" msg "feat(engine): $(printf 'x%.0s' $(seq 1 70))\n\n$sob\n" "the limit is 72"
expect fail "header ends with a period" msg "docs: fix typo.\n\n$sob\n" "must not end with a period"
expect fail "no blank line after header" msg "docs: fix typo\nmore text\n\n$sob\n" "blank line"

author_name="Claude Dupont" author_email="claude@example.com"
expect pass "human whose name matches an agent" msg "docs: fix typo\n\nSigned-off-by: Claude Dupont <claude@example.com>\n"

author_name="dependabot[bot]" author_email="49699333+dependabot[bot]@users.noreply.github.com"
bot_sob="Signed-off-by: dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>"
expect fail "bot-only sign-off" msg "build(deps): bump x\n\n$bot_sob\n" "a human must sign off"
expect pass "bot author with human sign-off" msg "build(deps): bump x\n\n$bot_sob\n$sob\n"

author_name="Claude" author_email="noreply@anthropic.com"
expect fail "agent-only sign-off" msg "feat(engine): add runner\n\nSigned-off-by: Claude <noreply@anthropic.com>\n" "a human must sign off"
author_name="Ada Human" author_email="ada@example.com"

# --- PR bodies -------------------------------------------------------------------------------
expect fail "PR body with generated-with footer" text "## Summary\n\nAdd hooks.\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n" "agent attribution"
expect fail "PR body with session link" text "## Summary\n\nhttps://claude.ai/code/session_abc\n" "agent attribution"
expect pass "clean PR body" text "## Summary\n\nReject Co-authored-by trailers and require a human sign-off.\n"

# --- commit ranges (CI) ----------------------------------------------------------------------
g() { git -C "$work/repo" -c commit.gpgsign=false -c core.hooksPath=/dev/null "$@"; }
export GIT_AUTHOR_NAME="Ada Human" GIT_AUTHOR_EMAIL="ada@example.com"
export GIT_COMMITTER_NAME="Ada Human" GIT_COMMITTER_EMAIL="ada@example.com"
git init -q "$work/repo"
g commit -q --allow-empty -m "chore: start history" -m "$sob"
base=$(g rev-parse HEAD)
g checkout -q -b topic
g commit -q --allow-empty -m "feat(engine): add runner" -m "$sob"
g checkout -q -
g merge -q --no-ff --no-edit topic
expect_range pass "range skips merge commits" "$base" HEAD
g commit -q --allow-empty -m "fixup! feat(engine): add runner"
expect_range fail "range rejects fixup commits" "$base" HEAD "squash autosquash"

printf '%d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
