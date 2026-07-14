// Journal persistence: every applied fix session is written to
// <gitdir>/submend/journal.json so `submend undo` can restore the previous
// state even from a later process.
package fixes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/JaydenCJ/submend/internal/version"
)

// Journal is one recorded fix session.
type Journal struct {
	SchemaVersion int      `json:"schema_version"`
	Tool          string   `json:"tool"`
	Version       string   `json:"version"`
	AppliedAt     string   `json:"applied_at"` // RFC 3339, informational only
	Actions       []Action `json:"actions"`
}

// ErrNoJournal is returned by LoadJournal when no fix session is recorded.
var ErrNoJournal = errors.New("no journal found: nothing to undo")

func journalPath(gitDir string) string {
	return filepath.Join(gitDir, "submend", "journal.json")
}

// SaveJournal writes the journal atomically (write + rename) under gitDir.
func SaveJournal(gitDir, appliedAt string, actions []Action) error {
	j := Journal{
		SchemaVersion: 1,
		Tool:          "submend",
		Version:       version.Version,
		AppliedAt:     appliedAt,
		Actions:       actions,
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	path := journalPath(gitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadJournal reads the recorded session, if any.
func LoadJournal(gitDir string) (*Journal, error) {
	data, err := os.ReadFile(journalPath(gitDir))
	if os.IsNotExist(err) {
		return nil, ErrNoJournal
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("corrupt journal %s: %w", journalPath(gitDir), err)
	}
	if j.SchemaVersion != 1 {
		return nil, fmt.Errorf("journal schema %d not supported by this submend", j.SchemaVersion)
	}
	return &j, nil
}

// RemoveJournal deletes the journal after a completed undo.
func RemoveJournal(gitDir string) error {
	err := os.Remove(journalPath(gitDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
