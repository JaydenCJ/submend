// Package fixes turns findings into an executable plan of safe git commands,
// applies them, and records an undo journal so `submend undo` can restore
// the previous state. Planning is pure; only Apply and Undo touch git.
package fixes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JaydenCJ/submend/internal/checks"
	"github.com/JaydenCJ/submend/internal/gitio"
	"github.com/JaydenCJ/submend/internal/scan"
)

// Action is one planned fix: the exact git commands to run (from the
// superproject root) and the exact commands that undo them.
type Action struct {
	Check      string     `json:"check"`
	Path       string     `json:"path"`
	Title      string     `json:"title"`
	Cmds       [][]string `json:"cmds"`
	Undo       [][]string `json:"undo,omitempty"`
	UndoNote   string     `json:"undo_note,omitempty"`
	Reversible bool       `json:"reversible"`
	SkipReason string     `json:"skip_reason,omitempty"` // non-empty: guarded off, not run
}

// Plan converts findings into ordered actions. Findings whose check has no
// automated fix are ignored (they stay visible in `submend doctor`).
// Guarded-off actions are returned with SkipReason set so the user sees why.
// `only` restricts planning to the given check IDs when non-empty.
func Plan(subs []scan.State, findings []checks.Finding, only map[string]bool) []Action {
	byPath := map[string]scan.State{}
	for _, s := range subs {
		byPath[s.Path] = s
	}
	var out []Action
	synced := map[string]bool{}    // paths already covered by a sync action
	headMoved := map[string]bool{} // paths whose HEAD an earlier action repositions
	for _, f := range findings {
		m := f.Meta()
		if !m.Fixable() {
			continue
		}
		if len(only) > 0 && !only[f.Check] {
			continue
		}
		s := byPath[f.Path]
		var a Action
		switch f.Check {
		case "SM02", "SM03":
			if synced[f.Path] {
				continue // one sync repairs both drift findings on this path
			}
			synced[f.Path] = true
			a = planSync(s, f.Check)
		case "SM01":
			a = planInit(s)
		case "SM12":
			a = planUpdate(s)
		case "SM04":
			a = planCheckout(s)
		case "SM05":
			a = planAttach(s)
		case "SM06":
			a = planRescue(s)
		case "SM11":
			a = planAbsorb(s)
		default:
			continue
		}
		// At most one action may reposition a submodule's HEAD per run:
		// e.g. an SM05 attach planned from pre-fix state would silently
		// revert the SM04 checkout that ran just before it.
		if movesHead(f.Check) && a.SkipReason == "" {
			if headMoved[f.Path] {
				a.SkipReason = "an earlier action in this plan already repositions HEAD here; " +
					"re-run `submend doctor` afterwards and fix again if needed"
			} else {
				headMoved[f.Path] = true
			}
		}
		out = append(out, a)
	}
	// Priority order (registry order), then path — URL fixes must land
	// before anything that fetches through those URLs.
	prio := map[string]int{}
	for i, m := range checks.Registry {
		prio[m.ID] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if prio[out[i].Check] != prio[out[j].Check] {
			return prio[out[i].Check] < prio[out[j].Check]
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func planSync(s scan.State, check string) Action {
	a := Action{
		Check: check, Path: s.Path,
		Title:      "sync submodule URL from .gitmodules",
		Cmds:       [][]string{{"submodule", "sync", "--", s.Path}},
		Reversible: true,
	}
	if s.URLConfig != "" {
		a.Undo = append(a.Undo, []string{"config", "submodule." + s.Name + ".url", s.URLConfig})
		a.UndoNote = "restores .git/config URL " + s.URLConfig
	}
	if s.Cloned && !s.RemoteMissing {
		a.Undo = append(a.Undo, []string{"-C", s.Path, "remote", "set-url", "origin", s.URLRemote})
	}
	return a
}

func planInit(s scan.State) Action {
	return Action{
		Check: "SM01", Path: s.Path,
		Title:      "initialize and clone the submodule",
		Cmds:       [][]string{{"submodule", "update", "--init", "--", s.Path}},
		Undo:       [][]string{{"submodule", "deinit", "--", s.Path}},
		UndoNote:   "deinit refuses to discard local modifications, so undo is loss-proof",
		Reversible: true,
	}
}

func planUpdate(s scan.State) Action {
	a := Action{
		Check: "SM12", Path: s.Path,
		Title:      "fetch and check out the recorded commit",
		Cmds:       [][]string{{"submodule", "update", "--", s.Path}},
		Reversible: true,
	}
	a.Undo, a.UndoNote = restoreHead(s)
	if s.Dirty {
		a.SkipReason = "worktree has uncommitted changes; commit or stash them first"
	}
	return a
}

func planCheckout(s scan.State) Action {
	a := Action{
		Check: "SM04", Path: s.Path,
		Title:      "check out the recorded commit " + short(s.GitlinkSHA),
		Cmds:       [][]string{{"-C", s.Path, "checkout", "--detach", s.GitlinkSHA}},
		Reversible: true,
	}
	a.Undo, a.UndoNote = restoreHead(s)
	switch {
	case s.Dirty:
		a.SkipReason = "worktree has uncommitted changes; commit or stash them first"
	case !s.HeadOnRef && s.Ahead != 0:
		a.SkipReason = fmt.Sprintf(
			"HEAD %s is on no branch and not contained in the recorded commit; "+
				"checking out would strand it — run `git -C %s branch keep-my-work` first",
			short(s.HeadSHA), s.Path)
	}
	return a
}

func planAttach(s scan.State) Action {
	branch := s.BranchesAtHead[0]
	return Action{
		Check: "SM05", Path: s.Path,
		Title:      "attach HEAD to branch " + branch + " (same commit)",
		Cmds:       [][]string{{"-C", s.Path, "checkout", branch}},
		Undo:       [][]string{{"-C", s.Path, "checkout", "--detach", s.HeadSHA}},
		UndoNote:   "detaches HEAD again at " + short(s.HeadSHA),
		Reversible: true,
	}
}

// movesHead reports whether a check's fix repositions the submodule's HEAD.
func movesHead(check string) bool {
	return check == "SM12" || check == "SM04" || check == "SM05"
}

func planRescue(s scan.State) Action {
	a := Action{
		Check: "SM06", Path: s.Path,
		Title: "create branch submend-rescue to keep " + short(s.HeadSHA) + " reachable",
		// The SHA is pinned explicitly so the branch lands on the stranded
		// commit even if another action repositions HEAD first.
		Cmds:       [][]string{{"-C", s.Path, "branch", "submend-rescue", s.HeadSHA}},
		Undo:       [][]string{{"-C", s.Path, "branch", "-D", "submend-rescue"}},
		UndoNote:   "deletes the rescue branch (the commits become unreachable again)",
		Reversible: true,
	}
	if s.RescueExists {
		a.SkipReason = "branch submend-rescue already exists in this submodule; rename or delete it first"
	}
	return a
}

func planAbsorb(s scan.State) Action {
	return Action{
		Check: "SM11", Path: s.Path,
		Title:      "absorb the embedded .git directory into .git/modules",
		Cmds:       [][]string{{"submodule", "absorbgitdirs", "--", s.Path}},
		UndoNote:   "not auto-undoable: git offers no un-absorb; history and worktree are untouched",
		Reversible: false,
	}
}

// restoreHead builds the undo commands that put a submodule's HEAD back
// where it was: the branch if one was checked out, else a detached checkout.
func restoreHead(s scan.State) ([][]string, string) {
	if s.HeadBranch != "" {
		return [][]string{{"-C", s.Path, "checkout", s.HeadBranch}},
			"checks out branch " + s.HeadBranch + " again"
	}
	if s.HeadSHA != "" {
		return [][]string{{"-C", s.Path, "checkout", "--detach", s.HeadSHA}},
			"detaches HEAD at " + short(s.HeadSHA) + " again"
	}
	return nil, "nothing was checked out before; undo leaves the fresh checkout in place"
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Runnable filters a plan down to the actions that will actually execute.
func Runnable(plan []Action) []Action {
	var out []Action
	for _, a := range plan {
		if a.SkipReason == "" {
			out = append(out, a)
		}
	}
	return out
}

// Apply executes every non-skipped action from the superproject root and
// returns the ones that ran. On the first failing command it stops and
// returns the actions applied so far together with the error, so the caller
// can still journal them for undo.
func Apply(r gitio.Runner, root string, plan []Action) ([]Action, error) {
	var applied []Action
	for _, a := range plan {
		if a.SkipReason != "" {
			continue
		}
		for _, cmd := range a.Cmds {
			if _, err := r.Run(root, cmd...); err != nil {
				return applied, fmt.Errorf("%s %s: git %s failed: %w",
					a.Check, a.Path, strings.Join(cmd, " "), err)
			}
		}
		applied = append(applied, a)
	}
	return applied, nil
}

// UndoResult reports what happened to one journaled action during undo.
type UndoResult struct {
	Action Action
	Note   string // set when the action could not be auto-undone
}

// Undo replays the undo commands of journaled actions in reverse order.
// Irreversible actions are reported, not attempted.
func Undo(r gitio.Runner, root string, actions []Action) ([]UndoResult, error) {
	var out []UndoResult
	for i := len(actions) - 1; i >= 0; i-- {
		a := actions[i]
		if len(a.Undo) == 0 {
			out = append(out, UndoResult{Action: a, Note: a.UndoNote})
			continue
		}
		for _, cmd := range a.Undo {
			if _, err := r.Run(root, cmd...); err != nil {
				return out, fmt.Errorf("undo %s %s: git %s failed: %w",
					a.Check, a.Path, strings.Join(cmd, " "), err)
			}
		}
		out = append(out, UndoResult{Action: a})
	}
	return out, nil
}
