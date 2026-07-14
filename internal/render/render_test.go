// Rendering tests: the text report is stable prose, the JSON envelope is a
// versioned contract — both are asserted against synthetic reports.
package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JaydenCJ/submend/internal/checks"
	"github.com/JaydenCJ/submend/internal/fixes"
	"github.com/JaydenCJ/submend/internal/scan"
)

func sampleResult() (*scan.Result, []checks.Finding) {
	res := &scan.Result{
		Root: "/work/demo", HeadDesc: "main @ 1a2b3c4",
		Subs: []scan.State{
			{Path: "libs/dep", Name: "libs/dep", GitlinkSHA: "aaaa", Initialized: true, Cloned: true},
			{Path: "vendor/ok", Name: "vendor/ok", Initialized: true, Cloned: true},
		},
	}
	findings := []checks.Finding{
		{Check: "SM02", Path: "libs/dep", Detail: []string{".gitmodules: new", ".git/config: old"}},
		{Check: "SM07", Path: "libs/dep", Detail: []string{"tracked files modified"}},
	}
	return res, findings
}

func TestDoctorTextListsFindingsWithFixTags(t *testing.T) {
	var buf bytes.Buffer
	res, findings := sampleResult()
	plan := []fixes.Action{{Check: "SM02", Path: "libs/dep",
		Cmds: [][]string{{"submodule", "sync", "--", "libs/dep"}}, Reversible: true}}
	DoctorText(&buf, res, findings, plan)
	out := buf.String()
	for _, want := range []string{
		"submend doctor — main @ 1a2b3c4 (2 submodules)",
		"SM02", "error", "URL in .git/config differs from .gitmodules",
		"fix: git submodule sync -- libs/dep   (reversible)", // the exact command fix would run
		"SM07", "manual:", // manual-only findings must show guidance, not a fix
		"2 findings (1 error, 1 warning, 0 info), 1 auto-fixable",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor text missing %q in:\n%s", want, out)
		}
	}
}

func TestDoctorTextShowsBlockedFixesAndDedupedOnes(t *testing.T) {
	res, _ := sampleResult()
	findings := []checks.Finding{
		{Check: "SM04", Path: "libs/dep", Detail: []string{"recorded aaaa, checked out bbbb"}},
		{Check: "SM03", Path: "libs/dep", Detail: []string{"origin drifted"}},
	}
	plan := []fixes.Action{{Check: "SM04", Path: "libs/dep",
		Cmds:       [][]string{{"-C", "libs/dep", "checkout", "--detach", "aaaa"}},
		SkipReason: "worktree has uncommitted changes; commit or stash them first"}}
	var buf bytes.Buffer
	DoctorText(&buf, res, findings, plan) // SM03 has no action: covered by a sync elsewhere
	out := buf.String()
	if !strings.Contains(out, "blocked: worktree has uncommitted changes") {
		t.Fatalf("blocked fix not surfaced:\n%s", out)
	}
	if !strings.Contains(out, "covered by another planned action") {
		t.Fatalf("deduped fix not explained:\n%s", out)
	}
}

func TestDoctorTextHealthyAndEmptyFooters(t *testing.T) {
	var healthy bytes.Buffer
	res, _ := sampleResult()
	DoctorText(&healthy, res, nil, nil)
	if !strings.Contains(healthy.String(), "all 2 submodules healthy — nothing to mend") {
		t.Fatalf("healthy footer missing:\n%s", healthy.String())
	}
	var empty bytes.Buffer
	DoctorText(&empty, &scan.Result{HeadDesc: "main @ 1a2b3c4"}, nil, nil)
	if !strings.Contains(empty.String(), "no submodules found") {
		t.Fatalf("empty-repo message missing:\n%s", empty.String())
	}
}

func TestDoctorJSONEnvelopeAndSummary(t *testing.T) {
	var buf bytes.Buffer
	res, findings := sampleResult()
	if err := DoctorJSON(&buf, res, findings); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if doc["tool"] != "submend" || doc["schema_version"] != float64(1) {
		t.Fatalf("envelope wrong: %v", doc)
	}
	sum := doc["summary"].(map[string]any)
	if sum["findings"] != float64(2) || sum["errors"] != float64(1) || sum["fixable"] != float64(1) {
		t.Fatalf("summary wrong: %v", sum)
	}
	// Healthy flags: libs/dep has findings, vendor/ok does not.
	subs := doc["submodules"].([]any)
	if subs[0].(map[string]any)["healthy"] != false || subs[1].(map[string]any)["healthy"] != true {
		t.Fatalf("healthy flags wrong: %v", subs)
	}
}

func TestDoctorJSONIsDeterministic(t *testing.T) {
	res, findings := sampleResult()
	var a, b bytes.Buffer
	_ = DoctorJSON(&a, res, findings)
	_ = DoctorJSON(&b, res, findings)
	if a.String() != b.String() {
		t.Fatal("identical input must render byte-identical JSON")
	}
}

func TestFixTextShowsCommandsSkipsAndJournal(t *testing.T) {
	plan := []fixes.Action{
		{Check: "SM02", Path: "libs/dep", Title: "sync submodule URL from .gitmodules",
			Cmds:     [][]string{{"submodule", "sync", "--", "libs/dep"}},
			UndoNote: "restores .git/config URL old", Reversible: true},
		{Check: "SM04", Path: "libs/dep", Title: "check out the recorded commit",
			Cmds:       [][]string{{"-C", "libs/dep", "checkout", "--detach", "aaaa"}},
			SkipReason: "worktree has uncommitted changes; commit or stash them first"},
	}
	var buf bytes.Buffer
	FixText(&buf, plan, plan[:1], false, "/repo/.git/submend/journal.json")
	out := buf.String()
	for _, want := range []string{
		"submend fix — 2 actions planned",
		"$ git submodule sync -- libs/dep",
		"undo: restores .git/config URL old",
		"applied",
		"skipped: worktree has uncommitted changes",
		"journal written to /repo/.git/submend/journal.json",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("fix text missing %q in:\n%s", want, out)
		}
	}
}

func TestFixTextDryRunNeverMentionsJournal(t *testing.T) {
	plan := []fixes.Action{{Check: "SM02", Path: "a", Title: "sync",
		Cmds: [][]string{{"submodule", "sync", "--", "a"}}}}
	var buf bytes.Buffer
	FixText(&buf, plan, nil, true, "/x")
	out := buf.String()
	if !strings.Contains(out, "dry run") || strings.Contains(out, "journal written") {
		t.Fatalf("dry-run output wrong:\n%s", out)
	}
}

func TestFixJSONStatuses(t *testing.T) {
	plan := []fixes.Action{
		{Check: "SM02", Path: "a", Cmds: [][]string{{"x"}}},
		{Check: "SM04", Path: "b", SkipReason: "dirty"},
	}
	var buf bytes.Buffer
	if err := FixJSON(&buf, plan, plan[:1], false); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Applied int `json:"applied"`
		Skipped int `json:"skipped"`
		Actions []struct {
			Status string `json:"status"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.Applied != 1 || doc.Skipped != 1 ||
		doc.Actions[0].Status != "applied" || doc.Actions[1].Status != "skipped" {
		t.Fatalf("statuses wrong: %+v", doc)
	}
}

func TestExplainTextCoversFixAndManualShapes(t *testing.T) {
	var buf bytes.Buffer
	ExplainText(&buf, checks.Registry)
	out := buf.String()
	for _, want := range []string{
		"SM02  config-url-drift  [error]",
		"automated fix: git submodule sync",
		"SM11", "undo: none — this fix is safe but one-way",
		"SM07", "manual remedy:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("explain text missing %q in:\n%s", want, out)
		}
	}
}

func TestUndoTextReportsIrreversibleLeftInPlace(t *testing.T) {
	results := []fixes.UndoResult{
		{Action: fixes.Action{Check: "SM11", Path: "a", Title: "absorb"}, Note: "one-way"},
		{Action: fixes.Action{Check: "SM02", Path: "b", Title: "sync",
			Undo: [][]string{{"config", "submodule.b.url", "old"}}}},
	}
	var buf bytes.Buffer
	UndoText(&buf, results, false)
	out := buf.String()
	for _, want := range []string{
		"left in place: one-way",
		"$ git config submodule.b.url old",
		"undone",
		"undo complete — journal removed",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("undo text missing %q in:\n%s", want, out)
		}
	}
}
