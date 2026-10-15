#!/usr/bin/env bash
# Works out the version a release should carry, so a person and the release workflow agree on it.
#
#   scripts/next-version.sh major|minor|patch|prerelease [current]
#   scripts/next-version.sh v1.2.3
#
# Without <current> it reads the highest v* tag in the repository, and starts from v0.0.0 when there
# is none. The bumps follow npm's: bumping the part a prerelease already anticipates releases it
# rather than moving past it, so patch on v1.2.3-rc1 is v1.2.3, and prerelease counts up from -0.
# Output is always canonical: vX.Y.Z or vX.Y.Z-PRE.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/next-version.sh <major|minor|patch|prerelease|version> [current]

  major        1.2.3 -> 2.0.0, and 1.0.0-rc1 -> 1.0.0
  minor        1.2.3 -> 1.3.0, and 1.2.0-rc1 -> 1.2.0
  patch        1.2.3 -> 1.2.4, and 1.2.3-rc1 -> 1.2.3
  prerelease   1.2.3 -> 1.2.4-0, 1.2.4-0 -> 1.2.4-1, 1.2.4-rc.1 -> 1.2.4-rc.2
  version      an exact version, such as v1.2.3 or 1.2.3-rc.1
USAGE
  exit 2
}

die() {
  printf 'next-version: %s\n' "$1" >&2
  exit 1
}

# semver, relaxed: the leading v is optional on input and always present on output. Build metadata
# (+foo) is not accepted, because a git tag carrying it would not round-trip through git describe.
readonly pattern='^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-([0-9A-Za-z.-]+))?$'

# latest prints the highest v* tag. git's version sort ranks v1.0.0-rc1 above v1.0.0 unless it is
# told that a hyphen starts a prerelease, which versionsort.suffix does.
latest() {
  git -c versionsort.suffix=- tag --list 'v*' --sort=-v:refname 2>/dev/null | head -1
}

bump=${1-}
[ -n "$bump" ] || usage

# An exact version needs nothing from the repository but the promise that it is free.
if [[ $bump =~ $pattern ]]; then
  want="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
  [ -z "${BASH_REMATCH[5]}" ] || want="$want-${BASH_REMATCH[5]}"
  if [ $# -lt 2 ] && git rev-parse -q --verify "refs/tags/$want" >/dev/null 2>&1; then
    die "$want is already tagged"
  fi
  printf '%s\n' "$want"
  exit 0
fi

if [ $# -ge 2 ]; then
  current=$2
  [ -n "$current" ] || die "the current version given is empty"
else
  current=$(latest)
  current=${current:-v0.0.0}
fi

[[ $current =~ $pattern ]] || die "the current version $current isn't a semantic version"
major=${BASH_REMATCH[1]}
minor=${BASH_REMATCH[2]}
patch=${BASH_REMATCH[3]}
pre=${BASH_REMATCH[5]}

case $bump in
major)
  # A prerelease of X.0.0 is already heading for X.0.0, so releasing it is the bump.
  if [ -z "$pre" ] || [ "$minor$patch" != "00" ]; then
    major=$((10#$major + 1))
  fi
  minor=0 patch=0 pre=""
  ;;
minor)
  if [ -z "$pre" ] || [ "$patch" != 0 ]; then
    minor=$((10#$minor + 1))
  fi
  patch=0 pre=""
  ;;
patch)
  if [ -z "$pre" ]; then
    patch=$((10#$patch + 1))
  fi
  pre=""
  ;;
prerelease)
  if [ -z "$pre" ]; then
    patch=$((10#$patch + 1))
    pre=0 # 1.2.3 -> 1.2.4-0
  elif [[ $pre =~ ^(.*[^0-9])?([0-9]+)$ ]]; then
    # Count up whatever trails it, keeping the identifier: 0 -> 1, rc1 -> rc2, rc.1 -> rc.2.
    pre="${BASH_REMATCH[1]-}$((10#${BASH_REMATCH[2]} + 1))"
  else
    pre="$pre.0" # 1.2.4-rc -> 1.2.4-rc.0
  fi
  ;;
*)
  usage
  ;;
esac

out="v$major.$minor.$patch"
[ -z "$pre" ] || out="$out-$pre"
printf '%s\n' "$out"
