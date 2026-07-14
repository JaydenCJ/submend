// Unit tests for fix planning (pure), the safety guards, and the journal
// round-trip. Actual git execution is covered by the CLI integration tests;
// here a recording fake runner asserts on the exact commands.
package fixes

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/JaydenCJ/submend/internal/checks"
	"github.com/JaydenCJ/submend/internal/scan"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	url  = "https://example.test/dep.git"
)

func baseState() scan.State {
	return scan.State{
		Name: "libs/dep", Path: "libs/dep",
		InGitmodules: true, URLModules: url, URLRaw: url,
		HasGitlink: true, GitlinkSHA: shaA,
		Initialized: true, URLConfig: url,
		Cloned: true, HeadSHA: shaA, HeadBranch: "main", URLRemote: url,
		GitlinkInSub: true, Ahead: -1, Behind: -1, HeadOnRef: true,
	}
}

func finding(check, path string) checks.Finding {
	return checks.Finding{Check: check, Path: path}
}

func plan1(t *testing.T, s scan.State, check string) Action {
	t.Helper()
	plan := Plan([]scan.State{s}, []checks.Finding{finding(check, s.Path)}, nil)
	if len(plan) != 1 {
		t.Fatalf("want 1 action, got %+v", plan)
	}
	return plan[0]
}

func TestPlanSyncUndoRestoresBothURLs(t *testing.T) {
	s := baseState()
	s.URLConfig = "https://old.example.test/dep.git"
	s.URLRemote = "https://old.example.test/dep.git"
	a := plan1(t, s, "SM02")
	if !reflect.DeepEqual(a.Cmds, [][]string{{"submodule", "sync", "--", "libs/dep"}}) {
		t.Fatalf("cmds = %v", a.Cmds)
	}
	wantUndo := [][]string{
		{"config", "submodule.libs/dep.url", "https://old.example.test/dep.git"},
		{"-C", "libs/dep", "remote", "set-url", "origin", "https://old.example.test/dep.git"},
	}
	if !reflect.DeepEqual(a.Undo, wantUndo) {
		t.Fatalf("undo = %v, want %v", a.Undo, wantUndo)
	}
	if !a.Reversible {
		t.Fatal("sync must be reversible")
	}
}

func TestPlanDeduplicatesSyncForSM02PlusSM03(t *testing.T) {
	// Both drift findings on one path are repaired by a single sync; two
	// sync actions would run the second against already-fixed state.
	s := baseState()
	s.URLConfig = "https://old.example.test/dep.git"
	s.URLRemote = "https://elsewhere.example.test/dep.git"
	plan := Plan([]scan.State{s},
		[]checks.Finding{finding("SM02", s.Path), finding("SM03", s.Path)}, nil)
	if len(plan) != 1 || plan[0].Check != "SM02" {
		t.Fatalf("want one deduplicated sync action, got %+v", plan)
	}
}

func TestPlanCheckoutGuardsDirtyWorktree(t *testing.T) {
	s := baseState()
	s.HeadSHA, s.Ahead, s.Behind, s.Dirty = shaB, 1, 0, true
	a := plan1(t, s, "SM04")
	if a.SkipReason == "" || !strings.Contains(a.SkipReason, "uncommitted") {
		t.Fatalf("dirty worktree must be guarded: %+v", a)
	}
}

func TestPlanCheckoutGuardsStrandedCommits(t *testing.T) {
	// HEAD holds commits on no branch and not contained in the recorded
	// commit: a checkout would strand them, so the plan must refuse.
	s := baseState()
	s.HeadSHA, s.HeadBranch, s.HeadOnRef, s.Ahead, s.Behind = shaB, "", false, 1, 0
	a := plan1(t, s, "SM04")
	if !strings.Contains(a.SkipReason, "strand") {
		t.Fatalf("stranding checkout must be guarded: %+v", a)
	}
}

func TestPlanCheckoutAllowsDetachedBehindHead(t *testing.T) {
	// Detached but strictly behind the recorded commit (ahead == 0): the
	// current HEAD stays reachable from the new checkout, so it is safe.
	s := baseState()
	s.HeadSHA, s.HeadBranch, s.HeadOnRef, s.Ahead, s.Behind = shaB, "", false, 0, 3
	a := plan1(t, s, "SM04")
	if a.SkipReason != "" {
		t.Fatalf("behind-only checkout should be allowed: %+v", a)
	}
	if !reflect.DeepEqual(a.Cmds, [][]string{{"-C", "libs/dep", "checkout", "--detach", shaA}}) {
		t.Fatalf("cmds = %v", a.Cmds)
	}
}

func TestPlanCheckoutUndoPrefersBranch(t *testing.T) {
	s := baseState()
	s.HeadSHA, s.Ahead, s.Behind = shaB, 1, 0 // on branch main
	a := plan1(t, s, "SM04")
	if !reflect.DeepEqual(a.Undo, [][]string{{"-C", "libs/dep", "checkout", "main"}}) {
		t.Fatalf("undo should restore the branch, got %v", a.Undo)
	}
}

func TestPlanAllowsOnlyOneHeadMovePerPath(t *testing.T) {
	// Detached HEAD, ahead of the gitlink, with a branch parked at the same
	// commit: SM04 checks out the recorded commit, so the SM05 attach —
	// planned from pre-fix state — must be skipped, or it would silently
	// revert the checkout.
	s := baseState()
	s.HeadSHA, s.HeadBranch, s.Ahead, s.Behind = shaB, "", 1, 0
	s.BranchesAtHead = []string{"feature"}
	plan := Plan([]scan.State{s},
		[]checks.Finding{finding("SM04", s.Path), finding("SM05", s.Path)}, nil)
	if len(plan) != 2 {
		t.Fatalf("want both actions in the plan, got %+v", plan)
	}
	if plan[0].Check != "SM04" || plan[0].SkipReason != "" {
		t.Fatalf("SM04 must run: %+v", plan[0])
	}
	if plan[1].Check != "SM05" || !strings.Contains(plan[1].SkipReason, "repositions HEAD") {
		t.Fatalf("SM05 must be skipped with an explanation: %+v", plan[1])
	}
}

func TestPlanRescuePinsStrandedSHA(t *testing.T) {
	s := baseState()
	s.HeadSHA, s.HeadBranch, s.HeadOnRef, s.Ahead, s.Behind = shaB, "", false, 1, 0
	a := plan1(t, s, "SM06")
	if !reflect.DeepEqual(a.Cmds, [][]string{{"-C", "libs/dep", "branch", "submend-rescue", shaB}}) {
		t.Fatalf("rescue branch must pin the stranded SHA: %v", a.Cmds)
	}
}

func TestPlanRescueGuardsExistingBranch(t *testing.T) {
	s := baseState()
	s.HeadBranch, s.HeadOnRef, s.RescueExists = "", false, true
	a := plan1(t, s, "SM06")
	if !strings.Contains(a.SkipReason, "already exists") {
		t.Fatalf("existing rescue branch must be guarded: %+v", a)
	}
}

func TestPlanAbsorbIsIrreversibleButExplained(t *testing.T) {
	s := baseState()
	s.EmbeddedGit = true
	a := plan1(t, s, "SM11")
	if a.Reversible || len(a.Undo) != 0 || a.UndoNote == "" {
		t.Fatalf("absorb must be marked one-way with an explanation: %+v", a)
	}
}

func TestPlanOrdersURLFixesBeforeFetchingFixes(t *testing.T) {
	// SM12's `submodule update` fetches through the configured URL, so the
	// SM02 sync must be planned first even if findings arrive interleaved.
	drift := baseState()
	drift.Path, drift.Name = "b/drift", "b/drift"
	drift.URLConfig = "https://old.example.test/x.git"
	missing := baseState()
	missing.Path, missing.Name = "a/missing", "a/missing"
	missing.GitlinkInSub = false
	plan := Plan([]scan.State{drift, missing},
		[]checks.Finding{finding("SM12", "a/missing"), finding("SM02", "b/drift")}, nil)
	if len(plan) != 2 || plan[0].Check != "SM02" || plan[1].Check != "SM12" {
		t.Fatalf("URL fix must come first: %+v", plan)
	}
}

func TestPlanOnlyFilterRestrictsChecks(t *testing.T) {
	s := baseState()
	s.URLConfig = "https://old.example.test/x.git"
	s.HeadSHA, s.Ahead, s.Behind = shaB, 0, 1
	findings := []checks.Finding{finding("SM02", s.Path), finding("SM04", s.Path)}
	plan := Plan([]scan.State{s}, findings, map[string]bool{"SM04": true})
	if len(plan) != 1 || plan[0].Check != "SM04" {
		t.Fatalf("--only filter ignored: %+v", plan)
	}
}

func TestPlanSkipsManualOnlyFindings(t *testing.T) {
	s := baseState()
	s.Dirty = true
	if plan := Plan([]scan.State{s}, []checks.Finding{finding("SM07", s.Path)}, nil); len(plan) != 0 {
		t.Fatalf("SM07 has no automated fix, plan = %+v", plan)
	}
}

func TestRunnableFiltersSkipped(t *testing.T) {
	plan := []Action{{Check: "SM04", SkipReason: "dirty"}, {Check: "SM02"}}
	r := Runnable(plan)
	if len(r) != 1 || r[0].Check != "SM02" {
		t.Fatalf("Runnable wrong: %+v", r)
	}
}

// fakeRunner records commands and optionally fails on a marker command.
type fakeRunner struct {
	calls  [][]string
	failOn string
}

func (f *fakeRunner) Run(dir string, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if f.failOn != "" && strings.Contains(strings.Join(args, " "), f.failOn) {
		return "", errors.New("boom")
	}
	return "", nil
}

func TestApplyRunsOnlyNonSkippedActions(t *testing.T) {
	r := &fakeRunner{}
	plan := []Action{
		{Check: "SM04", Path: "a", Cmds: [][]string{{"checkout"}}, SkipReason: "dirty"},
		{Check: "SM02", Path: "b", Cmds: [][]string{{"submodule", "sync", "--", "b"}}},
	}
	applied, err := Apply(r, "/repo", plan)
	if err != nil || len(applied) != 1 || applied[0].Check != "SM02" {
		t.Fatalf("applied = %+v, err = %v", applied, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("skipped action must not run: %v", r.calls)
	}
}

func TestApplyStopsOnFirstFailureAndReportsPartial(t *testing.T) {
	r := &fakeRunner{failOn: "second"}
	plan := []Action{
		{Check: "SM02", Path: "a", Cmds: [][]string{{"first"}}},
		{Check: "SM02", Path: "b", Cmds: [][]string{{"second"}}},
		{Check: "SM02", Path: "c", Cmds: [][]string{{"third"}}},
	}
	applied, err := Apply(r, "/repo", plan)
	if err == nil || len(applied) != 1 {
		t.Fatalf("partial failure must surface applied prefix: %+v, %v", applied, err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("apply must stop at the failure: %v", r.calls)
	}
}

func TestUndoReplaysInReverseOrder(t *testing.T) {
	r := &fakeRunner{}
	actions := []Action{
		{Check: "SM02", Path: "a", Undo: [][]string{{"undo-a"}}},
		{Check: "SM04", Path: "b", Undo: [][]string{{"undo-b"}}},
	}
	results, err := Undo(r, "/repo", actions)
	if err != nil || len(results) != 2 {
		t.Fatalf("results = %+v, err = %v", results, err)
	}
	if r.calls[0][0] != "undo-b" || r.calls[1][0] != "undo-a" {
		t.Fatalf("undo must run most-recent-first: %v", r.calls)
	}
}

func TestUndoReportsIrreversibleActionsWithoutRunning(t *testing.T) {
	r := &fakeRunner{}
	actions := []Action{{Check: "SM11", Path: "a", UndoNote: "one-way"}}
	results, err := Undo(r, "/repo", actions)
	if err != nil || len(results) != 1 || results[0].Note != "one-way" {
		t.Fatalf("results = %+v, err = %v", results, err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("irreversible action must not run commands: %v", r.calls)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	gitDir := t.TempDir()
	actions := []Action{{
		Check: "SM02", Path: "libs/dep", Title: "sync",
		Cmds:       [][]string{{"submodule", "sync", "--", "libs/dep"}},
		Undo:       [][]string{{"config", "submodule.libs/dep.url", url}},
		Reversible: true,
	}}
	if err := SaveJournal(gitDir, "2026-07-12T00:00:00Z", actions); err != nil {
		t.Fatalf("save: %v", err)
	}
	j, err := LoadJournal(gitDir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if j.SchemaVersion != 1 || j.Tool != "submend" || !reflect.DeepEqual(j.Actions, actions) {
		t.Fatalf("round-trip mismatch: %+v", j)
	}
	if err := RemoveJournal(gitDir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := LoadJournal(gitDir); !errors.Is(err, ErrNoJournal) {
		t.Fatalf("want ErrNoJournal after remove, got %v", err)
	}
	// Removing an already-missing journal must stay a no-op, not an error.
	if err := RemoveJournal(gitDir); err != nil {
		t.Fatalf("removing a missing journal must not fail: %v", err)
	}
}
