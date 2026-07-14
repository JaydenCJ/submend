# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- `doctor` subcommand diagnosing twelve submodule failure modes with stable
  IDs (SM01–SM12): uninitialized submodules, `.git/config` and origin-remote
  URL drift (including git's relative-URL resolution against the
  superproject origin), checkouts diverged from the recorded gitlink with
  ahead/behind counts, attachable detached HEADs, stranded commits reachable
  from no branch, dirty worktrees, untracked content, orphan gitlinks,
  orphan `.gitmodules` entries, embedded `.git` directories, and recorded
  commits missing from the clone's object store.
- `fix` subcommand applying only safe, explained repairs: `submodule sync`
  for URL drift, `update --init` for uninitialized paths, guarded checkout
  of the recorded commit (refuses on dirty worktrees or when commits would
  become unreachable), branch attachment at the same commit, a
  `submend-rescue` branch pinning stranded commits, and `absorbgitdirs`;
  with `--dry-run`, `--only ID`, and per-path HEAD-move deduplication.
- Undo journal (`.git/submend/journal.json`, schema v1) recording every
  applied action with its exact reverse commands; `undo` replays them
  most-recent-first and reports one-way fixes instead of guessing.
- `explain` subcommand documenting each check: what it means, why it bites,
  the automated fix, and how the fix is reverted.
- Pure git-config parser for `.gitmodules` (quotes, escapes, continuations,
  case rules) — no shelling out to read declarations.
- Text reports with concrete fix commands and severity summary; versioned
  JSON output (`schema_version: 1`) for doctor and fix.
- Linter-style exit codes: 0 healthy, 1 warnings/errors found (info-level
  advice alone does not fail), 2 usage, 3 runtime.
- Runnable examples (`examples/make-broken-repo.sh`,
  `examples/doctor-gate.sh`) and a check reference (`docs/checks.md`).
- 90 deterministic offline tests (unit + in-process CLI integration against
  fabricated superprojects) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/submend/releases/tag/v0.1.0
