# Contributing to submend

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22 and git ≥2.31 (for stable `for-each-ref --contains` and
`submodule` behavior); nothing else.

```bash
git clone https://github.com/JaydenCJ/submend && cd submend
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, fabricates a deterministic
superproject with real submodule problems in a temp dir, and asserts on
real CLI output across `doctor`, `fix` and `undo`; it must finish by
printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (90 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (checks and planning never shell out — only `gitio.System` does).

## Ground rules

- Keep dependencies at zero; adding one needs strong justification in the PR.
- No network calls, ever — submend's only external interface is the local
  `git` binary, and the only fetches it triggers go to remotes the user
  already configured. No telemetry.
- Safety invariants are non-negotiable: a fix must never touch uncommitted
  work, never make a commit unreachable, and every mutation is either
  journaled for `submend undo` or clearly labeled one-way.
- New checks are data plus one rule: add a `Meta` entry to the registry in
  `internal/checks/checks.go` (stable ID, severity, explanation, fix, undo),
  the rule in `diagnoseOne`, a planner in `internal/fixes` if fixable, and
  a row in `docs/checks.md` — each with tests reproducing the real repo shape.
- Code comments and doc comments are written in English.
- Determinism first: identical repository state must produce byte-identical
  reports, including all orderings.

## Reporting bugs

Include the output of `submend version`, the full command you ran, the
doctor report (`--format json` preferred; redact URLs if needed), and the
state git itself sees for the affected submodule:
`git submodule status <path>`, `git config --get-regexp '^submodule\.'`,
and `git -C <path> status --porcelain` — that is exactly what the scanner
reads.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
