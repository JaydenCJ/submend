// Subcommand implementations: doctor, fix, undo, explain.
package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JaydenCJ/submend/internal/checks"
	"github.com/JaydenCJ/submend/internal/fixes"
	"github.com/JaydenCJ/submend/internal/gitio"
	"github.com/JaydenCJ/submend/internal/render"
	"github.com/JaydenCJ/submend/internal/scan"
)

func runDoctor(r gitio.Runner, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor", stderr)
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return usageErr(stderr, err)
	}
	if !validFormat(*format) {
		return usageErr(stderr, fmt.Errorf("unknown --format %q (want text or json)", *format))
	}
	repo, ok := repoArg(fs, stderr)
	if !ok {
		return ExitUsage
	}
	res, err := scan.Scan(r, repo)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	findings := checks.Diagnose(res.Subs)
	if *format == "json" {
		if err := render.DoctorJSON(stdout, res, findings); err != nil {
			return runtimeErr(stderr, err)
		}
	} else {
		render.DoctorText(stdout, res, findings, fixes.Plan(res.Subs, findings, nil))
	}
	// Info-level findings are advice (e.g. a fresh clone's detached HEAD),
	// not failures: only warnings and errors flip the exit code.
	if sum := render.Summarize(res.Subs, findings); sum.Errors+sum.Warnings > 0 {
		return ExitFindings
	}
	return ExitOK
}

func runFix(r gitio.Runner, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("fix", stderr)
	format := fs.String("format", "text", "output format: text or json")
	dryRun := fs.Bool("dry-run", false, "plan and print, change nothing")
	onlyFlag := fs.String("only", "", "comma-separated check IDs to fix")
	if err := fs.Parse(args); err != nil {
		return usageErr(stderr, err)
	}
	if !validFormat(*format) {
		return usageErr(stderr, fmt.Errorf("unknown --format %q (want text or json)", *format))
	}
	only, err := parseOnly(*onlyFlag)
	if err != nil {
		return usageErr(stderr, err)
	}
	repo, ok := repoArg(fs, stderr)
	if !ok {
		return ExitUsage
	}
	res, err := scan.Scan(r, repo)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	findings := checks.Diagnose(res.Subs)
	plan := fixes.Plan(res.Subs, findings, only)

	var applied []fixes.Action
	var applyErr error
	if !*dryRun && len(fixes.Runnable(plan)) > 0 {
		applied, applyErr = fixes.Apply(r, res.Root, plan)
		if len(applied) > 0 {
			when := time.Now().UTC().Format(time.RFC3339)
			if err := fixes.SaveJournal(res.GitDir, when, applied); err != nil {
				return runtimeErr(stderr, fmt.Errorf("fixes applied but journal not written: %w", err))
			}
		}
	}
	journal := filepath.Join(res.GitDir, "submend", "journal.json")
	if *format == "json" {
		if err := render.FixJSON(stdout, plan, applied, *dryRun); err != nil {
			return runtimeErr(stderr, err)
		}
	} else {
		render.FixText(stdout, plan, applied, *dryRun, journal)
	}
	if applyErr != nil {
		return runtimeErr(stderr, applyErr)
	}
	return ExitOK
}

func runUndo(r gitio.Runner, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("undo", stderr)
	dryRun := fs.Bool("dry-run", false, "show what would be reverted, change nothing")
	if err := fs.Parse(args); err != nil {
		return usageErr(stderr, err)
	}
	repo, ok := repoArg(fs, stderr)
	if !ok {
		return ExitUsage
	}
	res, err := scan.Scan(r, repo)
	if err != nil {
		return runtimeErr(stderr, err)
	}
	j, err := fixes.LoadJournal(res.GitDir)
	if err != nil {
		if errors.Is(err, fixes.ErrNoJournal) {
			fmt.Fprintln(stdout, "submend undo — no journal found, nothing to undo")
			return ExitOK
		}
		return runtimeErr(stderr, err)
	}
	if *dryRun {
		var results []fixes.UndoResult
		for i := len(j.Actions) - 1; i >= 0; i-- {
			a := j.Actions[i]
			res := fixes.UndoResult{Action: a}
			if len(a.Undo) == 0 {
				res.Note = a.UndoNote
			}
			results = append(results, res)
		}
		render.UndoText(stdout, results, true)
		return ExitOK
	}
	results, undoErr := fixes.Undo(r, res.Root, j.Actions)
	render.UndoText(stdout, results, false)
	if undoErr != nil {
		return runtimeErr(stderr, undoErr)
	}
	if err := fixes.RemoveJournal(res.GitDir); err != nil {
		return runtimeErr(stderr, err)
	}
	return ExitOK
}

func runExplain(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 0:
		render.ExplainText(stdout, checks.Registry)
		return ExitOK
	case 1:
		id := strings.ToUpper(args[0])
		m, ok := checks.ByID(id)
		if !ok {
			var ids []string
			for _, m := range checks.Registry {
				ids = append(ids, m.ID)
			}
			sort.Strings(ids)
			fmt.Fprintf(stderr, "submend: unknown check %q (known: %s)\n", args[0], strings.Join(ids, ", "))
			return ExitUsage
		}
		render.ExplainText(stdout, []checks.Meta{m})
		return ExitOK
	default:
		fmt.Fprintln(stderr, "submend: explain takes at most one check ID")
		return ExitUsage
	}
}

// parseOnly validates a --only list against the registry.
func parseOnly(s string) (map[string]bool, error) {
	if s == "" {
		return nil, nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		id := strings.ToUpper(strings.TrimSpace(part))
		if id == "" {
			continue
		}
		m, ok := checks.ByID(id)
		if !ok {
			return nil, fmt.Errorf("--only: unknown check %q", part)
		}
		if !m.Fixable() {
			return nil, fmt.Errorf("--only: %s (%s) has no automated fix", m.ID, m.Name)
		}
		out[id] = true
	}
	if len(out) == 0 {
		return nil, errors.New("--only: no check IDs given")
	}
	return out, nil
}
