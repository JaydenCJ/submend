// Unit tests for the diagnostic rules, driven by synthetic scan states so
// every rule (and every deliberate non-firing) is pinned down exactly.
package checks

import (
	"strings"
	"testing"

	"github.com/JaydenCJ/submend/internal/scan"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	url  = "https://example.test/dep.git"
)

// healthy returns a submodule state that must trigger no findings: declared,
// initialized, cloned, on a branch, exactly at the recorded commit, clean.
func healthy() scan.State {
	return scan.State{
		Name: "libs/dep", Path: "libs/dep",
		InGitmodules: true, URLModules: url, URLRaw: url,
		HasGitlink: true, GitlinkSHA: shaA,
		Initialized: true, URLConfig: url,
		Cloned: true, HeadSHA: shaA, HeadBranch: "main", URLRemote: url,
		GitlinkInSub: true, Ahead: -1, Behind: -1, HeadOnRef: true,
	}
}

func ids(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Check)
	}
	return out
}

func assertIDs(t *testing.T, fs []Finding, want ...string) {
	t.Helper()
	got := ids(fs)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("findings = %v, want %v", got, want)
	}
}

func TestHealthySubmoduleHasNoFindings(t *testing.T) {
	assertIDs(t, Diagnose([]scan.State{healthy()}))
}

func TestUninitializedFiresSM01(t *testing.T) {
	s := healthy()
	s.Initialized, s.URLConfig, s.Cloned, s.HeadSHA, s.HeadBranch = false, "", false, "", ""
	fs := Diagnose([]scan.State{s})
	assertIDs(t, fs, "SM01")
	if !strings.Contains(fs[0].Detail[0], "not initialized") {
		t.Fatalf("detail should say why: %v", fs[0].Detail)
	}
}

func TestInitializedButNotClonedFiresSM01WithDistinctDetail(t *testing.T) {
	s := healthy()
	s.Cloned, s.HeadSHA, s.HeadBranch = false, "", ""
	fs := Diagnose([]scan.State{s})
	assertIDs(t, fs, "SM01")
	if !strings.Contains(fs[0].Detail[0], "never cloned") {
		t.Fatalf("detail should distinguish the not-cloned case: %v", fs[0].Detail)
	}
}

func TestConfigURLDriftFiresSM02(t *testing.T) {
	s := healthy()
	s.URLConfig = "https://old.example.test/dep.git"
	s.URLRemote = s.URLConfig // origin matches config, so no SM03
	fs := Diagnose([]scan.State{s})
	assertIDs(t, fs, "SM02")
	joined := strings.Join(fs[0].Detail, "\n")
	if !strings.Contains(joined, url) || !strings.Contains(joined, "old.example.test") {
		t.Fatalf("SM02 detail must quote both URLs: %v", fs[0].Detail)
	}
}

func TestRemoteURLDriftFiresSM03(t *testing.T) {
	s := healthy()
	s.URLRemote = "https://elsewhere.example.test/dep.git"
	assertIDs(t, Diagnose([]scan.State{s}), "SM03")
}

func TestUnresolvableRelativeURLSuppressesSM02(t *testing.T) {
	// A relative .gitmodules URL with no superproject origin cannot be
	// compared reliably; firing SM02 here would be a false positive.
	s := healthy()
	s.URLRaw, s.URLModules, s.Unresolvable = "../dep.git", "../dep.git", true
	assertIDs(t, Diagnose([]scan.State{s}))
}

func TestMissingRemoteSuppressesSM03(t *testing.T) {
	s := healthy()
	s.RemoteMissing, s.URLRemote = true, ""
	assertIDs(t, Diagnose([]scan.State{s}))
}

func TestOutOfSyncFiresSM04AndQuantifiesDivergence(t *testing.T) {
	cases := []struct {
		ahead, behind int
		want          string
	}{
		{2, 0, "2 commits ahead"},
		{0, 1, "1 commit behind"}, // singular wording
		{3, 2, "3 commits ahead and 2 commits behind"},
	}
	for _, c := range cases {
		s := healthy()
		s.HeadSHA, s.Ahead, s.Behind = shaB, c.ahead, c.behind
		fs := Diagnose([]scan.State{s})
		assertIDs(t, fs, "SM04")
		if joined := strings.Join(fs[0].Detail, "\n"); !strings.Contains(joined, c.want) {
			t.Errorf("ahead=%d behind=%d: want %q in %v", c.ahead, c.behind, c.want, fs[0].Detail)
		}
	}
}

func TestMissingCommitFiresSM12NotSM04(t *testing.T) {
	// When the recorded commit is absent from the clone, ahead/behind is
	// meaningless — SM12 must fire alone, never a confusing SM04 duplicate.
	s := healthy()
	s.GitlinkInSub, s.HeadSHA = false, shaB
	assertIDs(t, Diagnose([]scan.State{s}), "SM12")
}

func TestDetachedWithBranchAtHeadFiresSM05(t *testing.T) {
	s := healthy()
	s.HeadBranch = ""
	s.BranchesAtHead = []string{"main"}
	fs := Diagnose([]scan.State{s})
	assertIDs(t, fs, "SM05")
	if !strings.Contains(fs[0].Detail[0], `"main"`) {
		t.Fatalf("SM05 should name the branch: %v", fs[0].Detail)
	}
}

func TestDetachedAtRecordedCommitOnRefIsHealthy(t *testing.T) {
	// Plain detached HEAD at the recorded commit, reachable from a remote
	// branch: the normal post-`submodule update` state. Must stay silent.
	s := healthy()
	s.HeadBranch = ""
	assertIDs(t, Diagnose([]scan.State{s}))
}

func TestStrandedCommitsFireSM06(t *testing.T) {
	s := healthy()
	s.HeadBranch, s.HeadOnRef, s.HeadSHA, s.Ahead, s.Behind = "", false, shaB, 1, 0
	assertIDs(t, Diagnose([]scan.State{s}), "SM04", "SM06")
}

func TestDirtyAndUntrackedFireSeparately(t *testing.T) {
	s := healthy()
	s.Dirty = true
	assertIDs(t, Diagnose([]scan.State{s}), "SM07")
	s.Dirty, s.Untracked = false, true
	assertIDs(t, Diagnose([]scan.State{s}), "SM08")
}

func TestOrphansFireSM09AndSM10AndStopThere(t *testing.T) {
	// A gitlink with no declaration, and a declaration with no gitlink:
	// each fires exactly its orphan check and suppresses everything else.
	gitlinkOnly := scan.State{Path: "vendor/ghost", HasGitlink: true, GitlinkSHA: shaA, Ahead: -1, Behind: -1}
	assertIDs(t, Diagnose([]scan.State{gitlinkOnly}), "SM09")
	configOnly := scan.State{Name: "ghost", Path: "ghost", InGitmodules: true, URLModules: url, Ahead: -1, Behind: -1}
	assertIDs(t, Diagnose([]scan.State{configOnly}), "SM10")
}

func TestEmbeddedGitdirFiresSM11(t *testing.T) {
	s := healthy()
	s.EmbeddedGit = true
	assertIDs(t, Diagnose([]scan.State{s}), "SM11")
}

func TestFindingsSortedByPathThenPriority(t *testing.T) {
	b := healthy()
	b.Path = "b/dep"
	b.Dirty = true
	a := healthy()
	a.Path = "a/dep"
	a.URLConfig = "https://old.example.test/x.git"
	a.URLRemote = a.URLConfig
	a.Dirty = true
	fs := Diagnose([]scan.State{b, a})
	assertIDs(t, fs, "SM02", "SM07", "SM07")
	if fs[0].Path != "a/dep" || fs[2].Path != "b/dep" {
		t.Fatalf("path ordering wrong: %+v", fs)
	}
}

func TestRegistryIsInternallyConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Registry {
		if seen[m.ID] {
			t.Fatalf("duplicate check ID %s", m.ID)
		}
		seen[m.ID] = true
		if m.Summary == "" || m.Name == "" {
			t.Fatalf("%s: metadata incomplete", m.ID)
		}
		if !m.Fixable() && m.Manual == "" {
			t.Fatalf("%s: manual-only checks must carry manual guidance", m.ID)
		}
		if m.Undo != "" && m.Fix == "" {
			t.Fatalf("%s: undo without a fix makes no sense", m.ID)
		}
	}
	// ByID resolves every registered ID and rejects unknown ones.
	if m, ok := ByID("SM04"); !ok || m.Name != "out-of-sync" {
		t.Fatalf("ByID(SM04) = %+v, %v", m, ok)
	}
	if _, ok := ByID("SM99"); ok {
		t.Fatal("unknown ID must not resolve")
	}
}
