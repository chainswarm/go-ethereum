#!/usr/bin/env bash
# T1 (unit) for the chainswarm fork of go-ethereum: build everything, then test
# the packages our patches touch.
#
# This fork never edits .github (AP1000 local-ci-gate, design C10): upstream's
# workflows are switched off with `gh workflow disable`, and this script plus
# ci/manifest.json are the only files the fork adds. The local gate (AP1000
# scripts/ci-local.sh) is the one caller.
#
# What runs: `go build ./...` (outside the budget: a slow compile is not a slow
# test), then `go test -count=1` over the "packages" list of ci/manifest.json.
# That list is the set of Go packages whose files differ from upstream/master
# (`git log upstream/master..HEAD --name-only`). When the upstream ref is
# present, the script checks the list against it and fails on a patched package
# the list misses: a new patch gets its package added here before it merges.
#
# Artifacts: .ci/t1/go-test.log. The .ci/ directory ignores itself
# (.ci/.gitignore), so no upstream file is edited.
# Prints exactly one budget line: "T1 test phase: <n>s (budget 900s)".
set -euo pipefail

cd "$(dirname "$0")/.."
budget=900
out=.ci/t1
mkdir -p "$out"
[ -f .ci/.gitignore ] || echo '*' > .ci/.gitignore

mapfile -t packages < <(python3 -c '
import json
for p in json.load(open("ci/manifest.json"))["tiers"]["t1"]["packages"]:
    print(p)
')
[ "${#packages[@]}" -gt 0 ] || { echo "ci/manifest.json names no packages" >&2; exit 1; }

# The patched-package drift check (only when the upstream ref is fetched).
if git rev-parse --verify -q upstream/master >/dev/null; then
  missing=0
  while IFS= read -r dir; do
    [ -n "$dir" ] || continue
    pkg="./$dir"
    found=0
    for p in "${packages[@]}"; do [ "$p" = "$pkg" ] && found=1; done
    if [ "$found" = 0 ]; then
      echo "patched package $pkg is not in ci/manifest.json" >&2
      missing=1
    fi
  done < <(git log upstream/master..HEAD --name-only --format= -- '*.go' | xargs -r -n1 dirname | sort -u)
  [ "$missing" = 0 ] || exit 1
else
  echo "upstream/master not fetched: skipping the patched-package drift check"
fi

# Build everything and compile the tests (outside the budget).
go build ./...
go test -count=1 -run '^$' "${packages[@]}" >/dev/null

set +e
start=$(date +%s)
go test -count=1 "${packages[@]}" 2>&1 | tee "$out/go-test.log"
test_status=${PIPESTATUS[0]}
seconds=$(( $(date +%s) - start ))
set -e

echo "T1 test phase: ${seconds}s (budget ${budget}s)"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  echo "T1 test phase: ${seconds}s (budget ${budget}s)" >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$test_status" -ne 0 ]; then exit "$test_status"; fi
if [ "$seconds" -gt "$budget" ]; then
  echo "T1 took ${seconds}s, over the ${budget}s budget" >&2
  exit 1
fi
