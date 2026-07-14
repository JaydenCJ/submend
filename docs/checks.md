# Check reference

Every diagnosis submend can make, with its automated fix (if any) and how
that fix is undone. `submend explain <ID>` prints the same material with
the full background prose. IDs are stable: scripts may match on them.

## How checks relate to git's data model

A submodule lives in four places at once, and every check below is a
disagreement between two of them:

1. **`.gitmodules`** — the committed declaration (name, path, URL, branch).
2. **`.git/config`** — the initialized copy (`submodule.<name>.url`),
   written once by `git submodule init` and never refreshed by git.
3. **the index** — the *gitlink*, a mode-160000 entry recording the exact
   commit the superproject wants.
4. **the worktree clone** — what is actually checked out, with its own
   HEAD, branches, remotes, and possibly uncommitted changes.

## The checks

| ID | Name | Severity | Fires when | Automated fix | Undo |
|---|---|---|---|---|---|
| SM01 | uninitialized | error | declared + committed, but no config URL or no clone | `git submodule update --init -- <path>` | `deinit` (refuses to discard changes) |
| SM02 | config-url-drift | error | `.git/config` URL ≠ `.gitmodules` URL | `git submodule sync -- <path>` | restore both previous URLs |
| SM03 | remote-url-drift | warning | submodule origin ≠ configured URL | `git submodule sync -- <path>` | restore previous origin URL |
| SM04 | out-of-sync | warning | HEAD ≠ recorded gitlink | checkout of the recorded commit (guarded) | re-checkout previous branch/commit |
| SM05 | detached-attachable | info | detached, a local branch sits at HEAD | `git -C <path> checkout <branch>` | detach again at the same commit |
| SM06 | stranded-commits | warning | detached, HEAD on no local/remote branch | `git -C <path> branch submend-rescue <sha>` | delete the rescue branch |
| SM07 | dirty-worktree | warning | tracked files modified | — manual only | — |
| SM08 | untracked-content | info | untracked files present | — manual only | — |
| SM09 | orphan-gitlink | error | gitlink committed, no `.gitmodules` entry | — manual only | — |
| SM10 | orphan-config | warning | `.gitmodules` entry, no gitlink | — manual only | — |
| SM11 | embedded-gitdir | warning | `.git` is a directory, not a gitfile | `git submodule absorbgitdirs -- <path>` | none (safe but one-way) |
| SM12 | missing-commit | error | recorded commit absent from the clone | `git submodule update -- <path>` | re-checkout previous branch/commit |

## Safety guards on SM04 / SM12

The checkout fixes refuse to run (and say why) when:

- the submodule worktree has **uncommitted changes** — submend never
  touches uncommitted work;
- the current HEAD is on **no branch** and is **not contained** in the
  recorded commit — checking out would strand real commits. The suggested
  remedy is a rescue branch first, which `submend fix` creates itself when
  SM06 fires.

Additionally, at most one action per run may reposition a submodule's HEAD;
later HEAD-moving actions on the same path are skipped with an explanation,
because they were planned from pre-fix state.

## Deliberate non-findings

- **Detached HEAD at the recorded commit, reachable from a remote branch**
  is the normal state after `git submodule update` and is never reported.
- **Relative `.gitmodules` URLs** (`../dep.git`) are resolved against the
  superproject's `origin` using git's rules before comparison. When the
  superproject has no origin, the comparison is skipped entirely rather
  than risking a false SM02.
- **Info findings never flip the exit code** — a fresh
  `git clone --recurse-submodules` reports SM05 advice but still exits 0.

## The undo journal

Applied fixes are recorded in `.git/submend/journal.json`
(`schema_version: 1`), one entry per action with the exact commands run and
the exact reverse commands. `submend undo` replays the reverse commands
most-recent-first, reports one-way actions (SM11) instead of guessing, and
removes the journal when done. The journal lives inside `.git/`, so it is
never committed and never leaves the machine.
