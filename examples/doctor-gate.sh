#!/usr/bin/env bash
# A pre-push-style gate built on submend's JSON output: fails when any
# submodule has warnings or errors. Info-level advice does not block.
#
# Usage: bash examples/doctor-gate.sh [repo-path]
set -euo pipefail

REPO="${1:-.}"

# doctor exits 1 on warnings/errors by design; capture output either way.
JSON="$(submend doctor --format json "$REPO" || true)"

errors="$(echo "$JSON"   | grep -o '"errors": [0-9]*'   | grep -o '[0-9]*')"
warnings="$(echo "$JSON" | grep -o '"warnings": [0-9]*' | grep -o '[0-9]*')"

if [ "${errors:-0}" -gt 0 ] || [ "${warnings:-0}" -gt 0 ]; then
  echo "submodule gate: FAIL (errors: $errors, warnings: $warnings)" >&2
  echo "run \`submend doctor $REPO\` for details, \`submend fix $REPO\` for safe repairs" >&2
  exit 1
fi
echo "submodule gate: OK"
