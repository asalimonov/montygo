#!/usr/bin/env bash
# Checks scripts/version.sh against temporary git repositories.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$here/version.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CEILING_DIRECTORIES="$(dirname "$tmp")"
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export HOME="$tmp/home"
mkdir -p "$HOME"

fail=0
check() { # name expected actual
    if [[ "$2" == "$3" ]]; then
        echo "ok   $1: $3"
    else
        echo "FAIL $1: expected $2, got $3" >&2
        fail=1
    fi
}

commit() {
    echo "$1" > "file-$1"
    git add . >/dev/null
    git commit -q -m "$1"
}

# not a git repository
mkdir -p "$tmp/plain"
cd "$tmp/plain"
check "no repository" "0.0.0-unknown" "$("$script" 2>/dev/null)"
"$script" 2>&1 >/dev/null | grep -q "not a git repository" || { echo "FAIL no repository: warning missing" >&2; fail=1; }

mkdir -p "$tmp/repo"
cd "$tmp/repo"
git init -q -b main
commit one
hash=$(git rev-parse --short HEAD)

# no tag reachable
check "no tag" "0.0.0-$hash" "$("$script" 2>/dev/null)"
"$script" 2>&1 >/dev/null | grep -q "WARNING" || { echo "FAIL no tag: warning missing" >&2; fail=1; }

# tag at HEAD, highest wins, non-semver and non-v tags ignored
git tag v1.2.3
git tag v1.2.3-rc.1
git tag release-9
git tag 2.0.0
check "tag at HEAD" "1.2.3" "$("$script")"

# prerelease tag at HEAD
commit two
git tag v1.3.0-beta.2
check "prerelease at HEAD" "1.3.0-beta.2" "$("$script")"

# tag behind HEAD
commit three
hash=$(git rev-parse --short HEAD)
check "tag behind" "1.3.0-beta.2-$hash" "$("$script")"

# an unreachable tag on another branch is ignored
git checkout -q -b side HEAD~1
commit side
git tag v9.9.9
git checkout -q main
check "unreachable tag" "1.3.0-beta.2-$hash" "$("$script")"

# dirty tree: a modified tracked file
echo changed > file-one
check "dirty modified" "1.3.0-beta.2-$hash-dirty" "$("$script")"
git checkout -q -- file-one

# dirty tree: an untracked file
touch untracked
check "dirty untracked" "1.3.0-beta.2-$hash-dirty" "$("$script")"
rm untracked

# semver ordering of tags at HEAD
git tag v1.10.0
git tag v1.9.0
check "highest at HEAD" "1.10.0" "$("$script")"

# semver_cmp cases
# shellcheck source=version.sh
source "$script"
check "cmp equal" "0" "$(semver_cmp 1.0.0 1.0.0)"
check "cmp patch" "-1" "$(semver_cmp 1.0.0 1.0.1)"
check "cmp minor" "1" "$(semver_cmp 1.10.0 1.9.0)"
check "cmp prerelease lower" "-1" "$(semver_cmp 1.0.0-alpha 1.0.0)"
check "cmp prerelease numeric" "-1" "$(semver_cmp 1.0.0-alpha.1 1.0.0-alpha.beta)"
check "cmp prerelease longer" "1" "$(semver_cmp 1.0.0-alpha.1 1.0.0-alpha)"
check "cmp build metadata" "0" "$(semver_cmp 1.0.0+a 1.0.0+b)"

exit $fail
