#!/usr/bin/env bash
# End-to-end smoke test for submend: builds the binary, fabricates a
# deterministic superproject with real submodule problems, and asserts on
# the real CLI output across doctor / fix / undo. No network, idempotent,
# finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

# Assert that $2 contains $1. Never pipe the binary straight into `grep -q`:
# under pipefail, grep exiting on the first match would SIGPIPE the writer
# and turn a passing check into a flaky failure.
has() {
  grep -q -- "$1" <<<"$2"
}

BIN="$WORKDIR/submend"
DEP="$WORKDIR/dep"
DEP2="$WORKDIR/dep2"
SUPER="$WORKDIR/super"

# Isolate git completely from the host user's configuration. The file
# protocol is only allowed because every "remote" here is a local temp dir.
export GIT_CONFIG_GLOBAL="$WORKDIR/gitconfig"
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_DATE="2026-01-01T10:00:00+00:00"
export GIT_COMMITTER_DATE="2026-01-01T10:00:00+00:00"
cat > "$GIT_CONFIG_GLOBAL" <<'EOF'
[user]
	name = Dev Human
	email = dev@example.test
[init]
	defaultBranch = main
[protocol "file"]
	allow = always
[commit]
	gpgsign = false
EOF

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/submend) || fail "go build failed"

echo "2. version matches manifest"
[ "$("$BIN" --version)" = "submend 0.1.0" ] || fail "--version mismatch"

echo "3. fabricate a superproject with real submodule problems"
git init -q "$DEP"
echo "v1" > "$DEP/lib.txt"
git -C "$DEP" add -A && git -C "$DEP" commit -qm "dep: v1"
git init -q "$SUPER"
echo "hi" > "$SUPER/readme.txt"
git -C "$SUPER" add -A && git -C "$SUPER" commit -qm "super: init"
git -C "$SUPER" submodule add -q "$DEP" libs/dep
git -C "$SUPER" commit -qm "super: add submodule"
# Problem A (SM02): upstream "moved" — .gitmodules updated, .git/config stale.
git clone -q --bare "$DEP" "$DEP2"
git -C "$SUPER" config -f .gitmodules submodule.libs/dep.url "$DEP2"
git -C "$SUPER" commit -aqm "super: move dep upstream"
# Problem B (SM04): submodule HEAD moves one commit ahead of the gitlink.
RECORDED="$(git -C "$SUPER/libs/dep" rev-parse HEAD)"
echo "v2" > "$SUPER/libs/dep/lib.txt"
git -C "$SUPER/libs/dep" commit -aqm "dep: v2"

echo "4. doctor finds both problems and exits 1"
set +e
OUT="$("$BIN" doctor "$SUPER")"
CODE=$?
set -e
[ "$CODE" -eq 1 ] || fail "doctor should exit 1 on findings, got $CODE"
has "SM02" "$OUT" || fail "SM02 (config URL drift) missing"
has "SM04" "$OUT" || fail "SM04 (out of sync) missing"
has "1 commit ahead" "$OUT" || fail "divergence count missing"
has "2 findings" "$OUT" || fail "summary line missing"

echo "5. JSON report is machine-readable and versioned"
set +e
JSON="$("$BIN" doctor --format json "$SUPER")"
set -e
has '"tool": "submend"' "$JSON" || fail "json envelope missing"
has '"schema_version": 1' "$JSON" || fail "schema_version missing"
has '"fixable": 2' "$JSON" || fail "fixable count wrong"

echo "6. fix applies both actions and writes the undo journal"
FIXOUT="$("$BIN" fix "$SUPER")"
has "journal written" "$FIXOUT" || fail "fix did not journal"
[ "$(git -C "$SUPER" config submodule.libs/dep.url)" = "$DEP2" ] \
  || fail "config URL not synced"
[ "$(git -C "$SUPER/libs/dep" rev-parse HEAD)" = "$RECORDED" ] \
  || fail "submodule HEAD not restored to gitlink"
"$BIN" doctor "$SUPER" >/dev/null || fail "doctor should be clean after fix"

echo "7. undo restores the pre-fix state from the journal"
UNDOOUT="$("$BIN" undo "$SUPER")"
has "undo complete" "$UNDOOUT" || fail "undo did not complete"
[ "$(git -C "$SUPER" config submodule.libs/dep.url)" = "$DEP" ] \
  || fail "undo did not restore config URL"
[ "$(git -C "$SUPER/libs/dep" symbolic-ref --short HEAD)" = "main" ] \
  || fail "undo did not re-attach the branch"
has "nothing to undo" "$("$BIN" undo "$SUPER")" || fail "journal should be gone"

echo "8. dry-run plans without changing anything"
has "dry run" "$("$BIN" fix --dry-run "$SUPER")" || fail "dry-run banner missing"
[ ! -e "$SUPER/.git/submend/journal.json" ] || fail "dry-run wrote a journal"
[ "$(git -C "$SUPER/libs/dep" symbolic-ref --short HEAD)" = "main" ] \
  || fail "dry-run mutated the submodule"

echo "9. explain documents a check"
has "stranded-commits" "$("$BIN" explain SM06)" || fail "explain SM06 wrong"

echo "10. usage errors exit 2"
set +e
"$BIN" doctor --format yaml "$SUPER" >/dev/null 2>&1
[ $? -eq 2 ] || fail "bad --format should exit 2"
set -e

echo "SMOKE OK"
