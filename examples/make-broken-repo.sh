#!/usr/bin/env bash
# Fabricates a superproject whose submodules exhibit the classic failure
# modes submend diagnoses: an uninitialized submodule (SM01), URL drift
# after an upstream move (SM02), a checkout ahead of the recorded commit
# (SM04), and a dirty worktree (SM07). Entirely offline: every "remote" is
# a local directory. Never touches your global git configuration.
set -euo pipefail

TARGET="${1:-/tmp/submend-demo}"
rm -rf "$TARGET"
mkdir -p "$TARGET/upstream"

# Pinned identity and dates so the demo is reproducible; -c flags keep the
# host's git configuration untouched.
export GIT_AUTHOR_NAME="Dev Human" GIT_AUTHOR_EMAIL="dev@example.test"
export GIT_COMMITTER_NAME="Dev Human" GIT_COMMITTER_EMAIL="dev@example.test"
export GIT_AUTHOR_DATE="2026-01-01T10:00:00+00:00"
export GIT_COMMITTER_DATE="2026-01-01T10:00:00+00:00"
g() { git -c protocol.file.allow=always -c commit.gpgsign=false "$@"; }

make_upstream() { # make_upstream <name> <file>
  g init -q -b main "$TARGET/upstream/$1"
  echo "$1 v1" > "$TARGET/upstream/$1/$2"
  g -C "$TARGET/upstream/$1" add -A
  g -C "$TARGET/upstream/$1" commit -qm "$1: v1"
}
make_upstream parser parser.txt
make_upstream blob blob.txt
make_upstream tools run.sh

SUPER="$TARGET/super"
g init -q -b main "$SUPER"
echo "demo superproject" > "$SUPER/readme.txt"
g -C "$SUPER" add -A
g -C "$SUPER" commit -qm "super: init"
g -C "$SUPER" submodule add -q "$TARGET/upstream/parser" libs/parser
g -C "$SUPER" submodule add -q "$TARGET/upstream/blob"   vendor/blob
g -C "$SUPER" submodule add -q "$TARGET/upstream/tools"  tools
g -C "$SUPER" commit -qm "super: add submodules"

# SM02 — upstream moved: .gitmodules gets the new URL, .git/config keeps
# the old one (exactly what happens to every already-initialized clone).
g clone -q --bare "$TARGET/upstream/parser" "$TARGET/upstream/parser-moved"
g -C "$SUPER" config -f .gitmodules submodule.libs/parser.url "$TARGET/upstream/parser-moved"
g -C "$SUPER" commit -aqm "super: parser upstream moved"

# SM04 — a local experiment leaves the submodule one commit ahead of the
# gitlink the superproject records.
echo "parser v2 (local experiment)" > "$SUPER/libs/parser/parser.txt"
g -C "$SUPER/libs/parser" commit -aqm "parser: local experiment"

# SM07 — uncommitted edits inside a submodule.
echo "blob edited, never committed" > "$SUPER/vendor/blob/blob.txt"

# SM01 — a teammate deinitialized tools; the gitlink and declaration stay.
g -C "$SUPER" submodule --quiet deinit -f -- tools

echo "broken superproject ready: $SUPER"
echo "try:  submend doctor $SUPER"
