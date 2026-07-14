// In-process CLI integration tests: every scenario fabricates a real git
// superproject with real submodules in a temp dir, runs the exact code path
// `main` runs, and asserts on output and exit codes. Fully offline — all
// "remotes" are local directories via the file protocol.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/submend/internal/gitio"
)

// gitEnv isolates git completely from the host user's configuration and
// pins identity, dates, default branch, and file-protocol permission so
// every repository fabricated below is byte-deterministic.
func gitEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "gitconfig")
	content := "[user]\n\tname = Dev Human\n\temail = dev@example.test\n" +
		"[init]\n\tdefaultBranch = main\n" +
		"[protocol \"file\"]\n\tallow = always\n" +
		"[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T10:00:00+00:00")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T10:00:00+00:00")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitio.System{}.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return out
}

// makeDep fabricates the submodule's "upstream" repository with one commit.
func makeDep(t *testing.T) string {
	t.Helper()
	dep := filepath.Join(t.TempDir(), "dep")
	git(t, ".", "init", "-q", dep)
	if err := os.WriteFile(filepath.Join(dep, "lib.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dep, "add", "-A")
	git(t, dep, "commit", "-q", "-m", "dep: v1")
	return dep
}

// makeSuper fabricates a superproject with dep added at libs/dep.
func makeSuper(t *testing.T, dep string) string {
	t.Helper()
	super := filepath.Join(t.TempDir(), "super")
	git(t, ".", "init", "-q", super)
	if err := os.WriteFile(filepath.Join(super, "readme.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, super, "add", "-A")
	git(t, super, "commit", "-q", "-m", "super: init")
	git(t, super, "submodule", "add", "-q", dep, "libs/dep")
	git(t, super, "commit", "-q", "-m", "super: add submodule")
	return super
}

// run executes the CLI in-process and captures both streams.
func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb strings.Builder
	code = Run(args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestHealthySuperprojectExitsZero(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	stdout, _, code := run(t, "doctor", super)
	if code != ExitOK {
		t.Fatalf("exit = %d, out:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "all 1 submodule healthy") {
		t.Fatalf("healthy footer missing:\n%s", stdout)
	}
}

func TestRepoWithoutSubmodules(t *testing.T) {
	gitEnv(t)
	repo := filepath.Join(t.TempDir(), "plain")
	git(t, ".", "init", "-q", repo)
	stdout, _, code := run(t, "doctor", repo)
	if code != ExitOK || !strings.Contains(stdout, "no submodules found") {
		t.Fatalf("exit = %d, out:\n%s", code, stdout)
	}
}

func TestNotARepoExitsRuntime(t *testing.T) {
	gitEnv(t)
	_, stderr, code := run(t, "doctor", t.TempDir())
	if code != ExitRuntime || !strings.Contains(stderr, "not inside a git repository") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestUninitializedDetectedAndFixed(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, ".", "clone", "-q", super, clone)

	stdout, _, code := run(t, "doctor", clone)
	if code != ExitFindings || !strings.Contains(stdout, "SM01") {
		t.Fatalf("SM01 expected, exit = %d:\n%s", code, stdout)
	}
	stdout, stderr, code := run(t, "fix", clone)
	if code != ExitOK {
		t.Fatalf("fix failed (%d): %s\n%s", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "$ git submodule update --init -- libs/dep") {
		t.Fatalf("fix must print the exact command:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(clone, "libs/dep/lib.txt")); err != nil {
		t.Fatalf("submodule not materialized: %v", err)
	}
}

func TestConfigURLDriftSyncAndUndoRoundTrip(t *testing.T) {
	gitEnv(t)
	dep := makeDep(t)
	super := makeSuper(t, dep)
	// Upstream "moved": .gitmodules now points at dep2, .git/config still at dep.
	dep2 := filepath.Join(t.TempDir(), "dep2")
	git(t, ".", "clone", "-q", "--bare", dep, dep2)
	git(t, super, "config", "-f", ".gitmodules", "submodule.libs/dep.url", dep2)
	git(t, super, "commit", "-aqm", "super: move dep upstream")

	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM02") {
		t.Fatalf("SM02 expected:\n%s", stdout)
	}
	if _, _, code := run(t, "fix", super); code != ExitOK {
		t.Fatal("fix failed")
	}
	if got := git(t, super, "config", "submodule.libs/dep.url"); got != dep2 {
		t.Fatalf("config URL not synced: %q", got)
	}
	if got := git(t, filepath.Join(super, "libs/dep"), "remote", "get-url", "origin"); got != dep2 {
		t.Fatalf("origin not synced: %q", got)
	}
	// Undo restores both URLs and removes the journal.
	stdout, stderr, code := run(t, "undo", super)
	if code != ExitOK {
		t.Fatalf("undo failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "undo complete — journal removed") {
		t.Fatalf("undo output wrong:\n%s", stdout)
	}
	if got := git(t, super, "config", "submodule.libs/dep.url"); got != dep {
		t.Fatalf("undo did not restore config URL: %q", got)
	}
	if got := git(t, filepath.Join(super, "libs/dep"), "remote", "get-url", "origin"); got != dep {
		t.Fatalf("undo did not restore origin: %q", got)
	}
}

func TestRemoteURLDriftDetected(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	git(t, filepath.Join(super, "libs/dep"), "remote", "set-url", "origin", "/elsewhere/dep")
	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM03") {
		t.Fatalf("SM03 expected:\n%s", stdout)
	}
	if !strings.Contains(stdout, "/elsewhere/dep") {
		t.Fatalf("SM03 must quote the drifted URL:\n%s", stdout)
	}
}

func TestOutOfSyncCheckoutFixAndUndo(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	sub := filepath.Join(super, "libs/dep")
	recorded := git(t, sub, "rev-parse", "HEAD")
	// New commit inside the submodule: HEAD moves ahead of the gitlink.
	if err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, sub, "commit", "-aqm", "dep: v2")

	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM04") ||
		!strings.Contains(stdout, "1 commit ahead") {
		t.Fatalf("SM04 with ahead count expected:\n%s", stdout)
	}
	if _, _, code := run(t, "fix", super); code != ExitOK {
		t.Fatal("fix failed")
	}
	if got := git(t, sub, "rev-parse", "HEAD"); got != recorded {
		t.Fatalf("HEAD not restored to gitlink: %q != %q", got, recorded)
	}
	// Undo re-attaches the branch that was checked out before the fix.
	if _, _, code := run(t, "undo", super); code != ExitOK {
		t.Fatal("undo failed")
	}
	if got := git(t, sub, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("undo should restore branch main, got %q", got)
	}
}

func TestDirtyWorktreeGuardsCheckoutFix(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	sub := filepath.Join(super, "libs/dep")
	if err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, sub, "commit", "-aqm", "dep: v2")
	if err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM04") || !strings.Contains(stdout, "SM07") {
		t.Fatalf("SM04+SM07 expected:\n%s", stdout)
	}
	stdout, _, _ = run(t, "fix", super)
	if !strings.Contains(stdout, "skipped: worktree has uncommitted changes") {
		t.Fatalf("dirty guard missing:\n%s", stdout)
	}
	if got, _ := os.ReadFile(filepath.Join(sub, "lib.txt")); string(got) != "edited\n" {
		t.Fatalf("fix touched a dirty worktree: %q", got)
	}
}

func TestStrandedCommitsRescueBranch(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	sub := filepath.Join(super, "libs/dep")
	git(t, sub, "checkout", "-q", "--detach", "HEAD")
	if err := os.WriteFile(filepath.Join(sub, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, sub, "add", "-A")
	git(t, sub, "commit", "-qm", "dep: experiment on no branch")
	stranded := git(t, sub, "rev-parse", "HEAD")

	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM06") {
		t.Fatalf("SM06 expected:\n%s", stdout)
	}
	// The dangerous SM04 checkout is guarded off; the rescue branch is not.
	stdout, _, _ = run(t, "fix", super)
	if !strings.Contains(stdout, "would strand it") {
		t.Fatalf("strand guard missing:\n%s", stdout)
	}
	if got := git(t, sub, "rev-parse", "refs/heads/submend-rescue"); got != stranded {
		t.Fatalf("rescue branch not created at HEAD: %q != %q", got, stranded)
	}
	// With the commits now reachable, a second fix can safely re-sync HEAD.
	if _, _, code := run(t, "fix", super); code != ExitOK {
		t.Fatal("second fix failed")
	}
	if got := git(t, sub, "rev-parse", "HEAD"); got == stranded {
		t.Fatal("second fix should have checked out the recorded commit")
	}
}

func TestDetachedAttachableInfoDoesNotFailDoctor(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, ".", "clone", "-q", "--recurse-submodules", super, clone)
	// Fresh recursive clone: submodule detached, local main at the same
	// commit. That is advice (SM05, info), not a failure.
	stdout, _, code := run(t, "doctor", clone)
	if code != ExitOK {
		t.Fatalf("info-only findings must exit 0, got %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "SM05") || !strings.Contains(stdout, `"main"`) {
		t.Fatalf("SM05 expected:\n%s", stdout)
	}
	// The fix attaches HEAD to main without touching the worktree.
	if _, _, code := run(t, "fix", clone); code != ExitOK {
		t.Fatal("fix failed")
	}
	sub := filepath.Join(clone, "libs/dep")
	if got := git(t, sub, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("HEAD not attached: %q", got)
	}
}

func TestOrphanGitlinkDetected(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	sha := git(t, super, "rev-parse", "HEAD")
	git(t, super, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/ghost")
	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM09") {
		t.Fatalf("SM09 expected:\n%s", stdout)
	}
	if !strings.Contains(stdout, "git rm --cached") {
		t.Fatalf("SM09 must include manual guidance:\n%s", stdout)
	}
}

func TestOrphanConfigDetected(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	git(t, super, "config", "-f", ".gitmodules", "submodule.ghost.path", "ghost")
	git(t, super, "config", "-f", ".gitmodules", "submodule.ghost.url", "https://example.test/ghost.git")
	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM10") {
		t.Fatalf("SM10 expected:\n%s", stdout)
	}
}

func TestEmbeddedGitdirAbsorbed(t *testing.T) {
	gitEnv(t)
	dep := makeDep(t)
	super := makeSuper(t, dep)
	// Old-style layout: clone directly into the tree, then `submodule add`
	// adopts the existing directory with its embedded .git.
	git(t, super, "clone", "-q", dep, "vendor/blob")
	git(t, super, "submodule", "add", "-q", dep, "vendor/blob")
	git(t, super, "commit", "-qm", "super: adopt vendor/blob")

	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM11") {
		t.Fatalf("SM11 expected:\n%s", stdout)
	}
	stdout, _, _ = run(t, "fix", super)
	if !strings.Contains(stdout, "$ git submodule absorbgitdirs -- vendor/blob") {
		t.Fatalf("absorb command missing:\n%s", stdout)
	}
	info, err := os.Stat(filepath.Join(super, "vendor/blob/.git"))
	if err != nil || info.IsDir() {
		t.Fatalf(".git should now be a gitfile: %v, dir=%v", err, info != nil && info.IsDir())
	}
	// Undo must report the absorb as left in place, not attempt magic.
	stdout, _, _ = run(t, "undo", super)
	if !strings.Contains(stdout, "left in place: not auto-undoable") {
		t.Fatalf("undo must explain the one-way fix:\n%s", stdout)
	}
}

func TestMissingCommitFetchedByFix(t *testing.T) {
	gitEnv(t)
	dep := makeDep(t)
	super := makeSuper(t, dep)
	// Upstream advances; the superproject records the new commit via
	// cacheinfo (gitlinks are never validated against local objects), but
	// the local submodule clone has never fetched it.
	if err := os.WriteFile(filepath.Join(dep, "lib.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dep, "commit", "-aqm", "dep: v2")
	newSha := git(t, dep, "rev-parse", "HEAD")
	git(t, super, "update-index", "--cacheinfo", "160000,"+newSha+",libs/dep")

	stdout, _, code := run(t, "doctor", super)
	if code != ExitFindings || !strings.Contains(stdout, "SM12") {
		t.Fatalf("SM12 expected:\n%s", stdout)
	}
	if _, _, code := run(t, "fix", super); code != ExitOK {
		t.Fatal("fix failed")
	}
	if got := git(t, filepath.Join(super, "libs/dep"), "rev-parse", "HEAD"); got != newSha {
		t.Fatalf("fix should fetch and check out %s, got %s", newSha, got)
	}
}

func TestFixDryRunChangesNothing(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	sub := filepath.Join(super, "libs/dep")
	if err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, sub, "commit", "-aqm", "dep: v2")
	head := git(t, sub, "rev-parse", "HEAD")

	stdout, _, code := run(t, "fix", "--dry-run", super)
	if code != ExitOK || !strings.Contains(stdout, "dry run") {
		t.Fatalf("dry-run output wrong (%d):\n%s", code, stdout)
	}
	if got := git(t, sub, "rev-parse", "HEAD"); got != head {
		t.Fatal("dry run mutated the submodule")
	}
	if _, err := os.Stat(filepath.Join(super, ".git/submend/journal.json")); err == nil {
		t.Fatal("dry run must not write a journal")
	}
}

func TestFixOnlyFilterAndUnknownID(t *testing.T) {
	gitEnv(t)
	dep := makeDep(t)
	super := makeSuper(t, dep)
	sub := filepath.Join(super, "libs/dep")
	git(t, sub, "remote", "set-url", "origin", "/elsewhere/dep") // SM03
	if err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, sub, "commit", "-aqm", "dep: v2") // SM04

	stdout, _, code := run(t, "fix", "--only", "SM03", super)
	if code != ExitOK || strings.Contains(stdout, "SM04") {
		t.Fatalf("--only leaked other checks (%d):\n%s", code, stdout)
	}
	if got := git(t, sub, "remote", "get-url", "origin"); got != dep {
		t.Fatalf("SM03 not fixed: %q", got)
	}
	_, stderr, code := run(t, "fix", "--only", "SM99", super)
	if code != ExitUsage || !strings.Contains(stderr, "unknown check") {
		t.Fatalf("unknown --only must be a usage error: %d %q", code, stderr)
	}
	_, stderr, code = run(t, "fix", "--only", "SM07", super)
	if code != ExitUsage || !strings.Contains(stderr, "no automated fix") {
		t.Fatalf("manual-only --only must be rejected: %d %q", code, stderr)
	}
}

func TestUndoWithoutJournal(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	stdout, _, code := run(t, "undo", super)
	if code != ExitOK || !strings.Contains(stdout, "nothing to undo") {
		t.Fatalf("exit = %d:\n%s", code, stdout)
	}
}

func TestUndoDryRunKeepsJournal(t *testing.T) {
	gitEnv(t)
	dep := makeDep(t)
	super := makeSuper(t, dep)
	git(t, filepath.Join(super, "libs/dep"), "remote", "set-url", "origin", "/elsewhere/dep")
	if _, _, code := run(t, "fix", super); code != ExitOK {
		t.Fatal("fix failed")
	}
	journal := filepath.Join(super, ".git/submend/journal.json")
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("journal missing: %v", err)
	}
	stdout, _, code := run(t, "undo", "--dry-run", super)
	if code != ExitOK || !strings.Contains(stdout, "dry run") {
		t.Fatalf("undo dry-run wrong (%d):\n%s", code, stdout)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatal("dry-run undo must keep the journal")
	}
	if got := git(t, filepath.Join(super, "libs/dep"), "remote", "get-url", "origin"); got != dep {
		t.Fatalf("dry-run undo mutated state: %q", got)
	}
}

func TestDoctorJSONFormat(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	git(t, filepath.Join(super, "libs/dep"), "remote", "set-url", "origin", "/elsewhere/dep")
	stdout, _, code := run(t, "doctor", "--format", "json", super)
	if code != ExitFindings {
		t.Fatalf("exit = %d", code)
	}
	var doc struct {
		Tool          string `json:"tool"`
		SchemaVersion int    `json:"schema_version"`
		Findings      []struct {
			Check    string `json:"check"`
			Severity string `json:"severity"`
			Fixable  bool   `json:"fixable"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if doc.Tool != "submend" || doc.SchemaVersion != 1 {
		t.Fatalf("envelope wrong: %+v", doc)
	}
	if len(doc.Findings) != 1 || doc.Findings[0].Check != "SM03" || !doc.Findings[0].Fixable {
		t.Fatalf("findings wrong: %+v", doc.Findings)
	}
}

func TestFixJSONFormat(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	git(t, filepath.Join(super, "libs/dep"), "remote", "set-url", "origin", "/elsewhere/dep")
	stdout, _, code := run(t, "fix", "--format", "json", super)
	if code != ExitOK {
		t.Fatalf("exit = %d:\n%s", code, stdout)
	}
	var doc struct {
		DryRun  bool `json:"dry_run"`
		Applied int  `json:"applied"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if doc.DryRun || doc.Applied != 1 {
		t.Fatalf("fix JSON wrong: %+v", doc)
	}
}

func TestBarePathArgumentMeansDoctor(t *testing.T) {
	gitEnv(t)
	super := makeSuper(t, makeDep(t))
	stdout, _, code := run(t, super)
	if code != ExitOK || !strings.Contains(stdout, "submend doctor") {
		t.Fatalf("bare path should run doctor (%d):\n%s", code, stdout)
	}
}

func TestVersionAndHelp(t *testing.T) {
	stdout, _, code := run(t, "version")
	if code != ExitOK || stdout != "submend 0.1.0\n" {
		t.Fatalf("version wrong: %q (%d)", stdout, code)
	}
	stdout2, _, _ := run(t, "--version")
	if stdout2 != stdout {
		t.Fatal("--version must match the version subcommand")
	}
	help, _, code := run(t, "help")
	if code != ExitOK || !strings.Contains(help, "Usage:") || !strings.Contains(help, "submend fix") {
		t.Fatalf("help wrong (%d):\n%s", code, help)
	}
}

func TestExplainSingleAndAll(t *testing.T) {
	stdout, _, code := run(t, "explain", "sm04")
	if code != ExitOK || !strings.Contains(stdout, "out-of-sync") {
		t.Fatalf("explain sm04 (case-insensitive) wrong (%d):\n%s", code, stdout)
	}
	if strings.Contains(stdout, "SM02") {
		t.Fatal("single explain must not dump the whole registry")
	}
	all, _, code := run(t, "explain")
	if code != ExitOK || !strings.Contains(all, "SM01") || !strings.Contains(all, "SM12") {
		t.Fatalf("explain-all should cover the registry (%d)", code)
	}
	_, stderr, code := run(t, "explain", "SM99")
	if code != ExitUsage || !strings.Contains(stderr, "unknown check") {
		t.Fatalf("unknown check must be a usage error: %d %q", code, stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	_, stderr, code := run(t, "--bogus")
	if code != ExitUsage || !strings.Contains(stderr, "unknown flag") {
		t.Fatalf("unknown flag: %d %q", code, stderr)
	}
	_, stderr, code = run(t, "doctor", "--format", "yaml", ".")
	if code != ExitUsage || !strings.Contains(stderr, "unknown --format") {
		t.Fatalf("bad format: %d %q", code, stderr)
	}
	_, stderr, code = run(t, "doctor", "a", "b")
	if code != ExitUsage || !strings.Contains(stderr, "at most one repository path") {
		t.Fatalf("extra args: %d %q", code, stderr)
	}
}

func TestMultipleSubmodulesSortedByPath(t *testing.T) {
	gitEnv(t)
	depA, depB := makeDep(t), makeDep(t)
	super := makeSuper(t, depA)
	git(t, super, "submodule", "add", "-q", depB, "aa/first")
	git(t, super, "commit", "-qm", "super: second submodule")
	git(t, filepath.Join(super, "aa/first"), "remote", "set-url", "origin", "/x")
	git(t, filepath.Join(super, "libs/dep"), "remote", "set-url", "origin", "/y")
	stdout, _, _ := run(t, "doctor", super)
	first := strings.Index(stdout, "aa/first")
	second := strings.Index(stdout, "libs/dep")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("findings not sorted by path:\n%s", stdout)
	}
}
