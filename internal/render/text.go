// Package render formats doctor reports and fix plans for humans (text)
// and machines (JSON). Rendering is pure: identical input produces
// byte-identical output.
package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/submend/internal/checks"
	"github.com/JaydenCJ/submend/internal/fixes"
	"github.com/JaydenCJ/submend/internal/scan"
)

// Summary tallies findings by severity.
type Summary struct {
	Submodules int `json:"submodules"`
	Findings   int `json:"findings"`
	Errors     int `json:"errors"`
	Warnings   int `json:"warnings"`
	Info       int `json:"info"`
	Fixable    int `json:"fixable"`
}

// Summarize counts findings for the report footer and the JSON envelope.
func Summarize(subs []scan.State, findings []checks.Finding) Summary {
	s := Summary{Submodules: len(subs), Findings: len(findings)}
	for _, f := range findings {
		m := f.Meta()
		switch m.Severity {
		case checks.Error:
			s.Errors++
		case checks.Warning:
			s.Warnings++
		default:
			s.Info++
		}
		if m.Fixable() {
			s.Fixable++
		}
	}
	return s
}

// DoctorText writes the human report. plan supplies the concrete fix
// command for each fixable finding, so what doctor prints is exactly what
// `submend fix` would run.
func DoctorText(w io.Writer, res *scan.Result, findings []checks.Finding, plan []fixes.Action) {
	fmt.Fprintf(w, "submend doctor — %s (%s)\n", res.HeadDesc, plural(len(res.Subs), "submodule"))
	if len(res.Subs) == 0 {
		fmt.Fprintf(w, "\nno submodules found — nothing to diagnose\n")
		return
	}
	actionFor := map[string]fixes.Action{}
	for _, a := range plan {
		actionFor[a.Check+"\x00"+a.Path] = a
	}
	byPath := map[string][]checks.Finding{}
	for _, f := range findings {
		byPath[f.Path] = append(byPath[f.Path], f)
	}
	for _, s := range res.Subs {
		fs := byPath[s.Path]
		if len(fs) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", s.Path)
		for _, f := range fs {
			m := f.Meta()
			fmt.Fprintf(w, "  %s  %-7s %s\n", m.ID, m.Severity, m.Summary)
			for _, line := range f.Detail {
				fmt.Fprintf(w, "        %s\n", line)
			}
			switch a, ok := actionFor[f.Check+"\x00"+f.Path]; {
			case ok && a.SkipReason != "":
				fmt.Fprintf(w, "        fix: git %s\n", strings.Join(a.Cmds[0], " "))
				fmt.Fprintf(w, "        blocked: %s\n", a.SkipReason)
			case ok:
				tag := "reversible"
				if !a.Reversible {
					tag = "safe but one-way"
				}
				fmt.Fprintf(w, "        fix: git %s   (%s)\n", strings.Join(a.Cmds[0], " "), tag)
			case m.Fixable():
				// e.g. SM03 when the same path's SM02 sync already covers it.
				fmt.Fprintf(w, "        fix: covered by another planned action (see `submend fix --dry-run`)\n")
			case m.Manual != "":
				fmt.Fprintf(w, "        manual: %s\n", strings.ReplaceAll(m.Manual, "<path>", f.Path))
			}
		}
	}
	sum := Summarize(res.Subs, findings)
	if sum.Findings == 0 {
		fmt.Fprintf(w, "\nall %s healthy — nothing to mend\n", plural(sum.Submodules, "submodule"))
		return
	}
	fmt.Fprintf(w, "\n%s scanned: %s (%s, %s, %d info), %d auto-fixable\n",
		plural(sum.Submodules, "submodule"), plural(sum.Findings, "finding"),
		plural(sum.Errors, "error"), plural(sum.Warnings, "warning"),
		sum.Info, sum.Fixable)
	fmt.Fprintf(w, "run `submend fix` to apply safe fixes, `submend explain <ID>` for background\n")
}

// FixText writes the fix plan / results. applied is nil in dry-run mode.
func FixText(w io.Writer, plan []fixes.Action, applied []fixes.Action, dryRun bool, journalPath string) {
	if len(plan) == 0 {
		fmt.Fprintf(w, "submend fix — nothing to do (no auto-fixable findings)\n")
		return
	}
	mode := ""
	if dryRun {
		mode = " (dry run — nothing will change)"
	}
	fmt.Fprintf(w, "submend fix — %s planned%s\n", plural(len(plan), "action"), mode)
	appliedSet := map[string]bool{}
	for _, a := range applied {
		appliedSet[a.Check+"\x00"+a.Path] = true
	}
	n := 0
	for _, a := range plan {
		n++
		fmt.Fprintf(w, "\n%d. %s %s — %s\n", n, a.Check, a.Path, a.Title)
		for _, cmd := range a.Cmds {
			fmt.Fprintf(w, "     $ git %s\n", strings.Join(cmd, " "))
		}
		if a.SkipReason != "" {
			fmt.Fprintf(w, "   skipped: %s\n", a.SkipReason)
			continue
		}
		if a.UndoNote != "" {
			fmt.Fprintf(w, "     undo: %s\n", a.UndoNote)
		}
		switch {
		case dryRun:
			fmt.Fprintf(w, "   planned\n")
		case appliedSet[a.Check+"\x00"+a.Path]:
			fmt.Fprintf(w, "   applied\n")
		default:
			fmt.Fprintf(w, "   not applied\n")
		}
	}
	if !dryRun && len(applied) > 0 {
		fmt.Fprintf(w, "\njournal written to %s — revert everything with `submend undo`\n", journalPath)
	}
}

// UndoText writes the undo results, most recent action first.
func UndoText(w io.Writer, results []fixes.UndoResult, dryRun bool) {
	mode := ""
	if dryRun {
		mode = " (dry run — nothing will change)"
	}
	fmt.Fprintf(w, "submend undo — reverting %s%s\n", plural(len(results), "action"), mode)
	for _, r := range results {
		a := r.Action
		fmt.Fprintf(w, "\n%s %s — %s\n", a.Check, a.Path, a.Title)
		if r.Note != "" {
			fmt.Fprintf(w, "   left in place: %s\n", r.Note)
			continue
		}
		for _, cmd := range a.Undo {
			fmt.Fprintf(w, "     $ git %s\n", strings.Join(cmd, " "))
		}
		if dryRun {
			fmt.Fprintf(w, "   planned\n")
		} else {
			fmt.Fprintf(w, "   undone\n")
		}
	}
	if !dryRun {
		fmt.Fprintf(w, "\nundo complete — journal removed\n")
	}
}

// ExplainText writes the long-form documentation for one or all checks.
func ExplainText(w io.Writer, metas []checks.Meta) {
	for i, m := range metas {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  %s  [%s]\n", m.ID, m.Name, m.Severity)
		fmt.Fprintf(w, "  meaning: %s\n", m.Summary)
		if m.Why != "" {
			fmt.Fprintf(w, "  why it matters: %s\n", m.Why)
		}
		if m.Fixable() {
			fmt.Fprintf(w, "  automated fix: %s\n", m.Fix)
			if m.Reversible() {
				fmt.Fprintf(w, "  undo: %s\n", m.Undo)
			} else {
				fmt.Fprintf(w, "  undo: none — this fix is safe but one-way\n")
			}
		}
		if m.Manual != "" {
			fmt.Fprintf(w, "  manual remedy: %s\n", m.Manual)
		}
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
