#!/usr/bin/env bash
# Checks that scripts/check-pins.sh passes on this tree and fails on a drifted copy.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)
script="$here/check-pins.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

files=(
    proto/PROTO_REV internal/buildinfo/buildinfo.go worker-wasm/Cargo.toml server/Cargo.toml server/src/version.rs
    docker/Dockerfile docker/pyclient.Dockerfile .github/workflows/ci.yml internal/worker/websocket.go
)
copy() {
    local f
    for f in "${files[@]}"; do
        mkdir -p "$tmp/$1/$(dirname "$f")"
        cp "$root/$f" "$tmp/$1/$f"
    done
}

fail=0

"$script" "$root" >/dev/null && echo "ok   clean tree passes" || { echo "FAIL clean tree" >&2; fail=1; }

copy rev
sed -i.bak 's/^\(.*UpstreamRev *= *\)"[0-9a-f]*"/\1"00000000"/' "$tmp/rev/internal/buildinfo/buildinfo.go" && rm "$tmp/rev/internal/buildinfo/buildinfo.go.bak"
if out=$("$script" "$tmp/rev" 2>&1); then
    echo "FAIL drifted UpstreamRev passed" >&2; fail=1
elif [[ "$out" == *"internal/buildinfo/buildinfo.go: expected UpstreamRev"* ]]; then
    echo "ok   drifted UpstreamRev fails: $out"
else
    echo "FAIL drifted UpstreamRev: unexpected message: $out" >&2; fail=1
fi

copy version
sed -i.bak 's/^\(.*MontyVersion *= *\)"[^"]*"/\1"9.9.9"/' "$tmp/version/internal/buildinfo/buildinfo.go" && rm "$tmp/version/internal/buildinfo/buildinfo.go.bak"
if out=$("$script" "$tmp/version" 2>&1); then
    echo "FAIL drifted MontyVersion passed" >&2; fail=1
elif [[ "$out" == *"internal/worker/websocket.go: expected"* && "$out" == *"worker-wasm/Cargo.toml: expected"* ]]; then
    echo "ok   drifted MontyVersion fails: $(echo "$out" | tr '\n' ' ')"
else
    echo "FAIL drifted MontyVersion: unexpected message: $out" >&2; fail=1
fi

exit $fail
