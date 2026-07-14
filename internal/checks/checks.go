// Package checks is the diagnostic rule set: a pure function from scanned
// submodule state to findings. Every check has a stable ID, a severity, a
// long-form explanation (surfaced by `submend explain`), and — where a safe
// automated fix exists — a description of that fix and whether it can be
// undone. No check ever mutates anything.
package checks

import (
	"fmt"
	"sort"

	"github.com/JaydenCJ/submend/internal/scan"
)

// Severity orders findings by how urgently they need attention.
type Severity int

const (
	Info Severity = iota
	Warning
	Error
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	default:
		return "info"
	}
}

// Meta is the static description of one check, independent of any repo.
type Meta struct {
	ID       string
	Name     string
	Severity Severity
	Summary  string // one line: what the finding means
	Why      string // why it bites people
	Fix      string // what `submend fix` does, "" if manual-only
	Undo     string // how the fix is reverted, "" if not auto-undoable
	Manual   string // guidance when submend won't touch it
}

// Reversible reports whether the automated fix can be undone by `submend undo`.
func (m Meta) Reversible() bool { return m.Fix != "" && m.Undo != "" }

// Fixable reports whether `submend fix` has an automated action for this check.
func (m Meta) Fixable() bool { return m.Fix != "" }

// Registry lists every check in fix-priority order: URL configuration first
// (later fixes fetch through those URLs), then materialization, then commit
// pointers, then hygiene.
var Registry = []Meta{
	{
		ID: "SM02", Name: "config-url-drift", Severity: Error,
		Summary: "URL in .git/config differs from .gitmodules",
		Why: "Someone updated .gitmodules (a repo move, an HTTPS→SSH switch) but " +
			"already-initialized clones keep the old URL in .git/config forever — " +
			"git never re-reads .gitmodules for them. Fetches then fail or hit the " +
			"wrong host on every machine that cloned before the change.",
		Fix:  "git submodule sync -- <path> (copies the .gitmodules URL into .git/config and the submodule's origin remote)",
		Undo: "restore the previous .git/config URL and the previous origin URL",
	},
	{
		ID: "SM03", Name: "remote-url-drift", Severity: Warning,
		Summary: "submodule's origin remote differs from the configured URL",
		Why: "The clone inside the submodule fetches from wherever its origin " +
			"points, not from what the superproject configured. A manual " +
			"`git remote set-url` inside the submodule silently diverges the two.",
		Fix:  "git submodule sync -- <path>",
		Undo: "restore the previous origin URL",
	},
	{
		ID: "SM01", Name: "uninitialized", Severity: Error,
		Summary: "submodule is declared but not initialized or not cloned",
		Why: "The tree has an empty directory where code should be. Builds fail " +
			"with confusing missing-file errors, and `git status` stays quiet " +
			"because an uninitialized submodule is technically 'clean'.",
		Fix:  "git submodule update --init -- <path>",
		Undo: "git submodule deinit -- <path> (refuses to run if you made local changes, so nothing is lost)",
	},
	{
		ID: "SM12", Name: "missing-commit", Severity: Error,
		Summary: "the recorded commit does not exist in the submodule's clone",
		Why: "The superproject pins a commit the local clone has never fetched — " +
			"typically someone pushed a superproject bump before pushing the " +
			"submodule, or the clone is stale. Checkout and CI both break with " +
			"'fatal: could not get <sha>'.",
		Fix:  "git submodule update -- <path> (fetches from the configured URL, then checks out the recorded commit)",
		Undo: "check out the previously checked-out commit or branch",
	},
	{
		ID: "SM04", Name: "out-of-sync", Severity: Warning,
		Summary: "checked-out commit differs from the commit the superproject records",
		Why: "What you build is not what the superproject pins. This is how " +
			"'works on my machine' happens with submodules: HEAD drifted after a " +
			"branch switch or an experiment, and every diff now shows the dreaded " +
			"'new commits' noise.",
		Fix: "git -C <path> checkout --detach <recorded-sha> — only when the worktree is clean " +
			"and no local commits would become unreachable",
		Undo: "check out the previously checked-out commit or branch",
	},
	{
		ID: "SM05", Name: "detached-attachable", Severity: Info,
		Summary: "HEAD is detached but a local branch points at the same commit",
		Why: "Detached HEAD is the normal submodule state, but if you meant to " +
			"work on a branch, committing now would strand the commit. A branch " +
			"already sits at this exact commit, so attaching costs nothing.",
		Fix:  "git -C <path> checkout <branch> (same commit, worktree untouched)",
		Undo: "git -C <path> checkout --detach <sha>",
	},
	{
		ID: "SM06", Name: "stranded-commits", Severity: Warning,
		Summary: "detached HEAD holds commits that are on no branch",
		Why: "Commits reachable only from a detached HEAD vanish from view after " +
			"the next `git submodule update` and are eventually garbage-collected. " +
			"This is the single most common way real work is lost in submodules.",
		Fix:  "git -C <path> branch submend-rescue (a branch at HEAD keeps the commits reachable; nothing is checked out)",
		Undo: "git -C <path> branch -D submend-rescue",
	},
	{
		ID: "SM11", Name: "embedded-gitdir", Severity: Warning,
		Summary: "submodule has an embedded .git directory instead of a gitfile",
		Why: "Old-style clones keep the object store inside the worktree, so " +
			"`git rm`/`git checkout` across the submodule boundary refuse to run " +
			"and deleting the directory deletes the history with it.",
		Fix:  "git submodule absorbgitdirs -- <path> (moves the .git directory into the superproject's .git/modules and leaves a gitfile pointer)",
		Undo: "", // one-way by design; git provides no un-absorb
	},
	{
		ID: "SM07", Name: "dirty-worktree", Severity: Warning,
		Summary: "submodule has uncommitted changes to tracked files",
		Manual: "Commit the changes inside the submodule (then bump the gitlink in " +
			"the superproject), or discard them with `git -C <path> restore .`. " +
			"submend never touches uncommitted work.",
	},
	{
		ID: "SM08", Name: "untracked-content", Severity: Info,
		Manual: "Inspect with `git -C <path> status`; add the files, ignore them in " +
			"the submodule's .gitignore, or delete them.",
		Summary: "submodule contains untracked files",
	},
	{
		ID: "SM09", Name: "orphan-gitlink", Severity: Error,
		Summary: "index records a gitlink with no matching .gitmodules entry",
		Why: "Fresh clones cannot initialize this submodule at all — there is no " +
			"URL to clone from. Usually a leftover from a hand-edited .gitmodules " +
			"or a half-finished `git rm`.",
		Manual: "Either re-declare it (`git submodule add <url> <path>`) or remove " +
			"the stale gitlink (`git rm --cached <path>`), then commit.",
	},
	{
		ID: "SM10", Name: "orphan-config", Severity: Warning,
		Summary: ".gitmodules declares a submodule with no gitlink in the index",
		Why: "The declaration is dead weight: `git submodule update` skips it, but " +
			"tooling that parses .gitmodules still trips over the phantom entry.",
		Manual: "Remove the block from .gitmodules (`git config -f .gitmodules " +
			"--remove-section submodule.<name>`) and commit, or restore the " +
			"gitlink if the removal was accidental.",
	},
}

// ByID returns the metadata for a check ID.
func ByID(id string) (Meta, bool) {
	for _, m := range Registry {
		if m.ID == id {
			return m, true
		}
	}
	return Meta{}, false
}

// Finding is one diagnosed problem on one submodule path.
type Finding struct {
	Check  string   // registry ID
	Path   string   // submodule path
	Detail []string // concrete evidence lines, already formatted
}

// Meta returns the finding's check metadata.
func (f Finding) Meta() Meta {
	m, _ := ByID(f.Check)
	return m
}

// Diagnose runs every check against every scanned submodule and returns
// findings sorted by path, then by registry (fix-priority) order.
func Diagnose(subs []scan.State) []Finding {
	var out []Finding
	for _, s := range subs {
		out = append(out, diagnoseOne(s)...)
	}
	prio := map[string]int{}
	for i, m := range Registry {
		prio[m.ID] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return prio[out[i].Check] < prio[out[j].Check]
	})
	return out
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func diagnoseOne(s scan.State) []Finding {
	var out []Finding
	add := func(id string, detail ...string) {
		out = append(out, Finding{Check: id, Path: s.Path, Detail: detail})
	}

	// Orphans first: each side missing its counterpart.
	if s.HasGitlink && !s.InGitmodules {
		add("SM09", fmt.Sprintf("gitlink %s has no [submodule] entry in .gitmodules", short(s.GitlinkSHA)))
		return out // nothing else is meaningful without a declaration
	}
	if s.InGitmodules && !s.HasGitlink {
		add("SM10", fmt.Sprintf("declared as %q in .gitmodules, but nothing is committed at this path", s.Name))
		return out
	}

	// URL drift. Skipped for unresolvable relative URLs to avoid false alarms.
	if s.Initialized && !s.Unresolvable && s.URLModules != s.URLConfig {
		add("SM02",
			".gitmodules: "+s.URLModules,
			".git/config: "+s.URLConfig)
	}
	if s.Cloned && !s.RemoteMissing && s.URLConfig != "" && s.URLRemote != s.URLConfig {
		add("SM03",
			"configured:  "+s.URLConfig,
			"origin:      "+s.URLRemote)
	}

	// Materialization.
	if !s.Initialized || !s.Cloned {
		why := "not initialized (no URL in .git/config)"
		if s.Initialized {
			why = "initialized but never cloned (worktree is empty)"
		}
		add("SM01", why)
		return out // worktree checks below need a clone
	}

	// Commit pointer.
	switch {
	case !s.GitlinkInSub:
		add("SM12", fmt.Sprintf("superproject records %s, which is absent from the clone's object store", short(s.GitlinkSHA)))
	case s.HeadSHA != "" && s.HeadSHA != s.GitlinkSHA:
		d := []string{
			fmt.Sprintf("recorded %s, checked out %s", short(s.GitlinkSHA), short(s.HeadSHA)),
		}
		if s.Ahead >= 0 && s.Behind >= 0 {
			d = append(d, fmt.Sprintf("submodule is %s of the recorded commit", aheadBehind(s.Ahead, s.Behind)))
		}
		add("SM04", d...)
	}

	// Detached-HEAD hygiene.
	if s.HeadSHA != "" && s.HeadBranch == "" {
		if len(s.BranchesAtHead) > 0 {
			add("SM05", fmt.Sprintf("detached at %s; local branch %q points at the same commit",
				short(s.HeadSHA), s.BranchesAtHead[0]))
		} else if !s.HeadOnRef {
			add("SM06", fmt.Sprintf("detached at %s, which no local or remote branch can reach", short(s.HeadSHA)))
		}
	}

	// Layout and worktree hygiene.
	if s.EmbeddedGit {
		add("SM11", ".git is a full directory; modern layouts use a gitfile pointing into .git/modules")
	}
	if s.Dirty {
		add("SM07", "tracked files have uncommitted modifications (git -C "+s.Path+" status)")
	}
	if s.Untracked {
		add("SM08", "untracked files present (git -C "+s.Path+" status)")
	}
	return out
}

func aheadBehind(ahead, behind int) string {
	plural := func(n int) string {
		if n == 1 {
			return "1 commit"
		}
		return fmt.Sprintf("%d commits", n)
	}
	switch {
	case ahead > 0 && behind > 0:
		return plural(ahead) + " ahead and " + plural(behind) + " behind"
	case ahead > 0:
		return plural(ahead) + " ahead"
	default:
		return plural(behind) + " behind"
	}
}
