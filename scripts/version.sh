#!/usr/bin/env bash
# Prints the montygo version of the working tree.
#
#   vX.Y.Z[-pre] tag at HEAD        X.Y.Z[-pre]
#   tag reachable from HEAD         <highest tag>-<short hash>
#   no tag reachable                0.0.0-<short hash>, with a stderr warning
#   uncommitted changes             <any of the above>-dirty
#   not a git repository            0.0.0-unknown, with a stderr warning
#
# Tags are compared as loose semver; prereleases are allowed.
set -euo pipefail

is_loose_semver() {
    [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]
}

strip_v() {
    printf '%s\n' "${1#v}"
}

strip_build_metadata() {
    printf '%s\n' "${1%%+*}"
}

# Echoes -1, 0 or 1 for A < B, A == B, A > B per semver 2.0.0 §11.
semver_cmp() {
    local a b a_base b_base a_pre b_pre
    a=$(strip_build_metadata "$1")
    b=$(strip_build_metadata "$2")

    a_base="${a%%-*}"; a_pre=""
    [[ "$a" == *-* ]] && a_pre="${a#*-}"
    b_base="${b%%-*}"; b_pre=""
    [[ "$b" == *-* ]] && b_pre="${b#*-}"

    local aM am ap bM bm bp
    IFS='.' read -r aM am ap <<<"$a_base"
    IFS='.' read -r bM bm bp <<<"$b_base"

    local pair x y
    for pair in "$aM $bM" "$am $bm" "$ap $bp"; do
        read -r x y <<<"$pair"
        if (( x < y )); then printf '%s\n' -1; return; fi
        if (( x > y )); then printf '%s\n' 1; return; fi
    done

    if [[ -z "$a_pre" && -z "$b_pre" ]]; then printf '%s\n' 0; return; fi
    if [[ -z "$a_pre" ]]; then printf '%s\n' 1; return; fi
    if [[ -z "$b_pre" ]]; then printf '%s\n' -1; return; fi

    local a_ids b_ids
    IFS='.' read -ra a_ids <<<"$a_pre"
    IFS='.' read -ra b_ids <<<"$b_pre"
    local i n_a=${#a_ids[@]} n_b=${#b_ids[@]}
    local n_min=$(( n_a < n_b ? n_a : n_b ))
    for (( i = 0; i < n_min; i++ )); do
        local ai="${a_ids[i]}" bi="${b_ids[i]}"
        local ai_num=0 bi_num=0
        [[ "$ai" =~ ^[0-9]+$ ]] && ai_num=1
        [[ "$bi" =~ ^[0-9]+$ ]] && bi_num=1
        if (( ai_num && bi_num )); then
            if (( ai < bi )); then printf '%s\n' -1; return; fi
            if (( ai > bi )); then printf '%s\n' 1; return; fi
        elif (( ai_num != bi_num )); then
            if (( ai_num )); then printf '%s\n' -1; return; fi
            printf '%s\n' 1; return
        else
            if [[ "$ai" < "$bi" ]]; then printf '%s\n' -1; return; fi
            if [[ "$ai" > "$bi" ]]; then printf '%s\n' 1; return; fi
        fi
    done
    if (( n_a < n_b )); then printf '%s\n' -1; return; fi
    if (( n_a > n_b )); then printf '%s\n' 1; return; fi
    printf '%s\n' 0
}

# Echoes the highest loose-semver v* tag selected by the git tag arguments, or nothing.
pick_highest_tag() {
    local tags raw candidate best=""
    tags=$(git tag "$@" --list 'v*' 2>/dev/null || true)
    [[ -z "$tags" ]] && return 0
    while IFS= read -r raw; do
        [[ -z "$raw" ]] && continue
        candidate=$(strip_v "$raw")
        is_loose_semver "$candidate" || continue
        if [[ -z "$best" ]] || [[ "$(semver_cmp "$candidate" "$best")" == "1" ]]; then
            best="$candidate"
        fi
    done <<<"$tags"
    printf '%s\n' "$best"
}

pick_highest_at_head() {
    pick_highest_tag --points-at HEAD
}

pick_highest_reachable() {
    pick_highest_tag --merged HEAD
}

is_dirty() {
    [[ -n "$(git status --porcelain 2>/dev/null)" ]]
}

main() {
    if ! git rev-parse --git-dir >/dev/null 2>&1; then
        echo "version.sh: not a git repository; using 0.0.0-unknown" >&2
        printf '%s\n' "0.0.0-unknown"
        return 0
    fi
    local hash version at_head base
    hash=$(git rev-parse --short HEAD)
    at_head=$(pick_highest_at_head)
    if [[ -n "$at_head" ]]; then
        version="$at_head"
    else
        base=$(pick_highest_reachable)
        if [[ -n "$base" ]]; then
            version="$base-$hash"
        else
            echo "version.sh: WARNING: no v* tag reachable from HEAD; using 0.0.0-$hash" >&2
            version="0.0.0-$hash"
        fi
    fi
    if is_dirty; then
        version="$version-dirty"
    fi
    printf '%s\n' "$version"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
