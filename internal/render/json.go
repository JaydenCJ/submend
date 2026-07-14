// JSON rendering: a stable, versioned envelope (schema_version 1) so
// scripts and CI steps can consume doctor reports and fix plans.
package render

import (
	"encoding/json"
	"io"

	"github.com/JaydenCJ/submend/internal/checks"
	"github.com/JaydenCJ/submend/internal/fixes"
	"github.com/JaydenCJ/submend/internal/scan"
	"github.com/JaydenCJ/submend/internal/version"
)

type envelope struct {
	Tool          string `json:"tool"`
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
}

func env() envelope {
	return envelope{Tool: "submend", SchemaVersion: 1, Version: version.Version}
}

type jsonFinding struct {
	Check      string   `json:"check"`
	Name       string   `json:"name"`
	Severity   string   `json:"severity"`
	Path       string   `json:"path"`
	Summary    string   `json:"summary"`
	Detail     []string `json:"detail,omitempty"`
	Fixable    bool     `json:"fixable"`
	Reversible bool     `json:"reversible"`
}

type jsonSub struct {
	Path        string `json:"path"`
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
	Gitlink     string `json:"gitlink,omitempty"`
	Head        string `json:"head,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Initialized bool   `json:"initialized"`
	Cloned      bool   `json:"cloned"`
	Healthy     bool   `json:"healthy"`
}

type doctorDoc struct {
	envelope
	Repo       string        `json:"repo"`
	Submodules []jsonSub     `json:"submodules"`
	Findings   []jsonFinding `json:"findings"`
	Summary    Summary       `json:"summary"`
}

// DoctorJSON writes the machine-readable report.
func DoctorJSON(w io.Writer, res *scan.Result, findings []checks.Finding) error {
	unhealthy := map[string]bool{}
	jf := make([]jsonFinding, 0, len(findings))
	for _, f := range findings {
		m := f.Meta()
		unhealthy[f.Path] = true
		jf = append(jf, jsonFinding{
			Check: m.ID, Name: m.Name, Severity: m.Severity.String(),
			Path: f.Path, Summary: m.Summary, Detail: f.Detail,
			Fixable: m.Fixable(), Reversible: m.Reversible(),
		})
	}
	js := make([]jsonSub, 0, len(res.Subs))
	for _, s := range res.Subs {
		js = append(js, jsonSub{
			Path: s.Path, Name: s.Name, URL: s.URLModules,
			Gitlink: s.GitlinkSHA, Head: s.HeadSHA, Branch: s.HeadBranch,
			Initialized: s.Initialized, Cloned: s.Cloned,
			Healthy: !unhealthy[s.Path],
		})
	}
	doc := doctorDoc{
		envelope: env(), Repo: res.Root,
		Submodules: js, Findings: jf,
		Summary: Summarize(res.Subs, findings),
	}
	return writeJSON(w, doc)
}

type fixDoc struct {
	envelope
	DryRun  bool         `json:"dry_run"`
	Actions []jsonAction `json:"actions"`
	Applied int          `json:"applied"`
	Skipped int          `json:"skipped"`
}

type jsonAction struct {
	fixes.Action
	Status string `json:"status"` // planned | applied | skipped
}

// FixJSON writes the plan/result of a fix run.
func FixJSON(w io.Writer, plan []fixes.Action, applied []fixes.Action, dryRun bool) error {
	appliedSet := map[string]bool{}
	for _, a := range applied {
		appliedSet[a.Check+"\x00"+a.Path] = true
	}
	doc := fixDoc{envelope: env(), DryRun: dryRun, Actions: []jsonAction{}}
	for _, a := range plan {
		status := "planned"
		switch {
		case a.SkipReason != "":
			status = "skipped"
			doc.Skipped++
		case appliedSet[a.Check+"\x00"+a.Path]:
			status = "applied"
			doc.Applied++
		}
		doc.Actions = append(doc.Actions, jsonAction{Action: a, Status: status})
	}
	return writeJSON(w, doc)
}

func writeJSON(w io.Writer, doc any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
