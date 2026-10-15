#!/usr/bin/env bash
# Tests for next-version.sh. Run from anywhere: scripts/next-version.test.sh
set -uo pipefail

next="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

passed=0
failed=0
cwd=. # the repository a test runs in, for the cases that read git tags

record() { # <name> <ok|not> <detail>
  if [ "$2" = ok ]; then
    passed=$((passed + 1))
  else
    failed=$((failed + 1))
    printf 'FAIL: %s\n%s\n\n' "$1" "$3" >&2
  fi
}

# want <expected> <bump> [current]
want() {
  local expect=$1 got
  shift
  got=$(cd "$cwd" && "$next" "$@" 2>&1) || got="exited $?: $got"
  if [ "$got" = "$expect" ]; then
    record "next-version.sh $*" ok ""
  else
    record "next-version.sh $*" not "  want $expect
  got  $got"
  fi
}

# refuses <wanted exit code> <name> <args...>
refuses() {
  local code=$1 name=$2 out got
  shift 2
  out=$(cd "$cwd" && "$next" "$@" 2>&1)
  got=$?
  if [ "$got" = "$code" ]; then
    record "$name" ok ""
  else
    record "$name" not "  wanted exit $code, got $got: $out"
  fi
}

# The bumps, against npm's own semantics.
want v1.2.4 patch 1.2.3
want v1.3.0 minor 1.2.3
want v2.0.0 major 1.2.3
want v1.2.4-0 prerelease 1.2.3

# A prerelease is already heading somewhere: bumping that part releases it.
want v1.2.3 patch v1.2.3-rc1
want v1.2.0 minor v1.2.0-rc1
want v1.3.0 minor v1.2.3-rc1
want v1.0.0 major v1.0.0-rc1
want v2.0.0 major v1.2.3-rc1

# Counting up a prerelease keeps the identifier in front of the number.
want v1.2.4-1 prerelease v1.2.4-0
want v1.2.4-rc2 prerelease v1.2.4-rc1
want v1.2.4-rc.2 prerelease v1.2.4-rc.1
want v1.2.4-rc.10 prerelease v1.2.4-rc.9
want v1.2.4-rc.0 prerelease v1.2.4-rc

# The leading v is optional coming in and always there going out.
want v1.2.4 patch v1.2.3
want v2.0.0 v2.0.0 v1.0.0
want v2.0.0-rc.1 v2.0.0-rc.1 v1.0.0

# Nothing in the repository yet: the first release is named, or bumped from v0.0.0.
git init -q "$work/fresh" && git -C "$work/fresh" config tag.gpgsign false
cwd=$work/fresh
want v0.1.0 minor
want v0.0.1 patch
want v1.0.0 major
want v0.1.0 v0.1.0
cwd=.

# With tags, the highest is the one bumped, and git's own sort would rank the prerelease above it.
git init -q "$work/tagged" &&
  git -C "$work/tagged" config tag.gpgsign false &&
  git -C "$work/tagged" config user.email t@example.com &&
  git -C "$work/tagged" config user.name Tester &&
  git -C "$work/tagged" commit -q --allow-empty -m first
for tag in v0.1.0 v0.2.0 v0.2.1-0 v0.2.1-1 v0.2.1 v0.10.0; do git -C "$work/tagged" tag "$tag"; done
cwd=$work/tagged
want v0.10.1 patch
want v0.11.0 minor
want v1.0.0 major
want v0.10.1-0 prerelease
refuses 1 "a version that is already tagged" v0.2.1
want v0.2.2 v0.2.2
cwd=.

# A released version is higher than its own prereleases, which is only true if git is told that a
# hyphen starts one: here the release is the highest tag, so a bump has to count from it.
git init -q "$work/pre" &&
  git -C "$work/pre" config tag.gpgsign false &&
  git -C "$work/pre" config user.email t@example.com &&
  git -C "$work/pre" config user.name Tester &&
  git -C "$work/pre" commit -q --allow-empty -m first
for tag in v0.2.1-0 v0.2.1-1 v0.2.1; do git -C "$work/pre" tag "$tag"; done
cwd=$work/pre
want v0.2.2 patch
want v0.3.0 minor
want v0.2.2-0 prerelease
cwd=.

# What isn't a version, and what isn't a bump.
refuses 2 "no argument at all"
refuses 2 "an empty argument" ""
refuses 2 "a bump nobody defined" sideways
refuses 2 "a version missing a part" v1.2
refuses 2 "a version with four parts" v1.2.3.4
refuses 2 "build metadata git describe would lose" v1.2.3+build7
refuses 1 "a current version that isn't one" patch pineapple
refuses 1 "a current version that is empty" patch ""

printf '%d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
