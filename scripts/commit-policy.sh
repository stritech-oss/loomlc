#!/usr/bin/env bash
# commit-policy.sh: enforce loomlc's commit message policy (docs/contributing.md#commits).
#
# Usage:
#   scripts/commit-policy.sh msg <file>           check a commit message file (commit-msg hook)
#   scripts/commit-policy.sh range <base> <head>  check every non-merge commit in base..head (CI)
#   scripts/commit-policy.sh text <file>          check free text, e.g. a PR body, for agent attribution
#
# Rules:
#   1. The header is a Conventional Commit of at most 72 characters, has no trailing period, and is
#      followed by a blank line.
#   2. No Co-authored-by trailers, agent session trailers or links, or "generated with <agent>" lines.
#   3. The commit author signs off (a Signed-off-by with the author's email).
#   4. At least one sign-off is from a human; bot- or agent-only sign-offs are rejected.
#
# Stays portable to bash 3.2 (macOS): no associative arrays or ${var,,}.
set -euo pipefail

max_header=72
types='feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert'

# Bot and agent identities, matched case-insensitively against "Name <email>". Match on the email or
# the [bot] suffix only: a name alone (e.g. "Claude") can belong to a human.
bot_ident_re='\[bot\]|<noreply@anthropic\.com>|<noreply@openai\.com>|<[0-9]+\+copilot@users\.noreply\.github\.com>|<cursoragent@cursor\.com>'

# Agent attribution banned from commit messages and PR bodies, matched case-insensitively per line.
banned_res=(
  '^[[:space:]]*co-authored-by:'
  '^[[:space:]]*assisted-by:'
  # Any trailer naming a session, thread, conversation or chat: Claude-Session:, Codex-Thread-Id:, and
  # whatever the next agent calls the same thing.
  '^[[:space:]]*[a-z0-9-]*(session|thread|conversation|chat)[a-z0-9-]*:'
  # An attribution footer, not prose. loomlc's own docs discuss agents generating things constantly, so
  # this matches only a line that starts with the claim, or the robot emoji those footers carry.
  '^[[:space:]]*(🤖[[:space:]]*)?generated[ -](with|by|using)[[:space:]]'
  '^[[:space:]]*🤖'
  '^[[:space:]]*generated-(with|by|using):'
  'claude\.ai/(code|chat|share)([/)>[:space:]]|$)'
  'chatgpt\.com/(codex|c|share|g)([/)>[:space:]]|$)'
  'chat\.openai\.com/'
  'gemini\.google\.com/(app|share)'
  'g\.co/gemini/share'
  'jules\.google\.com/'
  'app\.devin\.ai/'
  'cursor\.com/agents'
)

failed=0
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

err() {
  printf 'commit-policy: %s\n' "$*" >&2
  failed=1
}

usage() {
  sed -n '/^# Usage:/,/^#$/p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

# Drop git's comment lines and everything below the scissors line of a verbose commit.
clean_message() {
  sed '/^# -\{24\} >8 -\{24\}$/,$d' "$1" | git stripspace --strip-comments
}

check_banned() { # <file> <context>
  local re hit
  for re in "${banned_res[@]}"; do
    if hit=$(grep -Ein -m1 -- "$re" "$1"); then
      err "$2: agent attribution is not allowed (line ${hit%%:*}): ${hit#*:}"
    fi
  done
}

is_bot() { # <"Name <email>">
  grep -Eiq -- "$bot_ident_re" <<<"$1"
}

check_message() { # <message-file> <author "Name <email>"> <context> <ci: 0|1>
  local file=$1 author=$2 ctx=$3 ci=$4
  local header second sobs author_email sob human=0

  check_banned "$file" "$ctx"

  header=$(sed -n 1p "$file")
  second=$(sed -n 2p "$file")
  if [ -z "$header" ]; then
    err "$ctx: empty commit message"
    return
  fi

  case $header in
    'fixup! '* | 'squash! '* | 'amend! '*)
      [ "$ci" = 0 ] || err "$ctx: squash autosquash commits before merging: \"$header\""
      return
      ;;
    'Merge '*) ;; # git-generated header, but still needs a sign-off
    'Revert "'*) ;; # git-generated header, but still needs a sign-off
    *)
      grep -Eq -- "^($types)(\([a-z0-9][a-z0-9._/-]*\))?!?: [^ ]" <<<"$header" ||
        err "$ctx: header is not a Conventional Commit: \"$header\" (expected type(scope): subject; types: ${types//|/, })"
      [ "${#header}" -le "$max_header" ] ||
        err "$ctx: header is ${#header} characters; the limit is $max_header"
      case $header in *.) err "$ctx: header must not end with a period" ;; esac
      ;;
  esac
  [ -z "$second" ] || err "$ctx: leave a blank line between the header and the body"

  sobs=$(git interpret-trailers --parse <"$file" | grep -i '^signed-off-by:' || true)
  if [ -z "$sobs" ]; then
    err "$ctx: missing Signed-off-by; commit with -s (expected \"Signed-off-by: $author\")"
    return
  fi
  author_email=$(sed -n 's/.*<\(.*\)>.*/\1/p' <<<"$author" | tr '[:upper:]' '[:lower:]')
  tr '[:upper:]' '[:lower:]' <<<"$sobs" | grep -Fq -- "<$author_email>" ||
    err "$ctx: no Signed-off-by from the commit author <$author_email>"
  while IFS= read -r sob; do
    is_bot "$sob" || human=1
  done <<<"$sobs"
  [ "$human" = 1 ] || err "$ctx: every Signed-off-by is a bot or agent; a human must sign off"
}

case ${1:-} in
  msg)
    [ $# -eq 2 ] || usage
    clean_message "$2" >"$tmp"
    check_message "$tmp" "$(git var GIT_AUTHOR_IDENT | sed 's/ [0-9]* [-+][0-9]*$//')" "commit message" 0
    ;;
  range)
    [ $# -eq 3 ] || usage
    for commit in $(git rev-list --no-merges --reverse "$2..$3"); do
      git log -1 --format=%B "$commit" >"$tmp"
      check_message "$tmp" "$(git log -1 --format='%an <%ae>' "$commit")" "$(git rev-parse --short "$commit")" 1
    done
    ;;
  text)
    [ $# -eq 2 ] || usage
    check_banned "$2" "text"
    ;;
  *)
    usage
    ;;
esac

if [ "$failed" = 1 ]; then
  printf 'commit-policy: see docs/contributing.md#commits\n' >&2
  exit 1
fi
[ "$1" = msg ] || echo "commit-policy: OK"
