#!/usr/bin/env bash
# Checks every file that repeats the upstream revision or the Monty version.
# Sources: proto/PROTO_REV (full SHA) and MontyVersion in internal/buildinfo/buildinfo.go.
# Usage: check-pins.sh [repository root]
set -euo pipefail

root="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$root"

REV=$(tr -d '[:space:]' < proto/PROTO_REV)
[[ "$REV" =~ ^[0-9a-f]{40}$ ]] || { echo "proto/PROTO_REV: expected a 40-character SHA, got \"$REV\"" >&2; exit 1; }
SHORT=${REV:0:8}
MV=$(sed -n 's/^[[:space:]]*MontyVersion *= *"\(.*\)".*/\1/p' internal/buildinfo/buildinfo.go)
[[ -n "$MV" ]] || { echo "internal/buildinfo/buildinfo.go: expected MontyVersion = \"X.Y.Z\"" >&2; exit 1; }

fail=0
expect() { # file pattern description
    grep -qE "$2" "$1" || { echo "$1: expected $3" >&2; fail=1; }
}
count() { # file pattern n description
    local n
    n=$(grep -cE "$2" "$1" || true)
    [[ "$n" -eq "$3" ]] || { echo "$1: expected $3 × $4, found $n" >&2; fail=1; }
}

expect internal/buildinfo/buildinfo.go "UpstreamRev *= *\"$SHORT\"" "UpstreamRev = \"$SHORT\""
count worker-wasm/Cargo.toml "rev = \"$SHORT\"" 3 "rev = \"$SHORT\""
count server/Cargo.toml "rev = \"$SHORT\"" 3 "rev = \"$SHORT\""
expect server/src/version.rs "MONTY_REV: &str = \"$REV\"" "MONTY_REV = \"$REV\""
expect docker/Dockerfile "^ARG MONTY_REV=$REV\$" "ARG MONTY_REV=$REV"
expect docker/pyclient.Dockerfile "^ARG MONTY_REV=$REV\$" "ARG MONTY_REV=$REV"
expect .github/workflows/ci.yml "MONTY_REV: $REV\$" "MONTY_REV: $REV"
expect internal/worker/websocket.go "\"monty-pool/$MV\"" "DefaultUserAgent \"monty-pool/$MV\""
expect worker-wasm/Cargo.toml "^version = \"$MV\"" "version = \"$MV\""

exit $fail
