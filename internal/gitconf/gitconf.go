// Package gitconf is a pure parser for the git-config file syntax, used to
// read .gitmodules without shelling out. It implements the subset git itself
// documents for hand-written files: sections and quoted subsections,
// case-insensitive section/key names, comments, quoted values with escapes,
// and backslash line continuations.
package gitconf

import (
	"fmt"
	"sort"
	"strings"
)

// Entry is one key/value pair with its section context. Section and Key are
// lower-cased (git treats them case-insensitively); Subsection keeps its
// exact case (git treats it case-sensitively).
type Entry struct {
	Section    string
	Subsection string
	Key        string
	Value      string
}

// Module is one `[submodule "<name>"]` block from a .gitmodules file.
type Module struct {
	Name   string
	Path   string
	URL    string
	Branch string
}

// Parse reads git-config syntax and returns entries in file order.
// Duplicate keys are preserved; callers decide precedence (git: last wins).
func Parse(src string) ([]Entry, error) {
	lines := strings.Split(src, "\n")
	var entries []Entry
	section, subsection := "", ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		lineNo := i + 1
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			var err error
			section, subsection, err = parseSectionHeader(line, lineNo)
			if err != nil {
				return nil, err
			}
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("line %d: key %q before any section header", lineNo, line)
		}
		key, rest, err := splitKey(line, lineNo)
		if err != nil {
			return nil, err
		}
		value, consumed, err := parseValue(rest, lines[i+1:], lineNo)
		if err != nil {
			return nil, err
		}
		i += consumed
		entries = append(entries, Entry{Section: section, Subsection: subsection, Key: key, Value: value})
	}
	return entries, nil
}

// parseSectionHeader parses `[name]` or `[name "subsection"]`.
func parseSectionHeader(line string, lineNo int) (section, subsection string, err error) {
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", "", fmt.Errorf("line %d: unterminated section header", lineNo)
	}
	if rest := strings.TrimSpace(line[end+1:]); rest != "" && rest[0] != '#' && rest[0] != ';' {
		return "", "", fmt.Errorf("line %d: trailing characters after section header", lineNo)
	}
	body := strings.TrimSpace(line[1:end])
	name, sub := body, ""
	if q := strings.IndexByte(body, '"'); q >= 0 {
		name = strings.TrimSpace(body[:q])
		raw := body[q:]
		if len(raw) < 2 || raw[len(raw)-1] != '"' {
			return "", "", fmt.Errorf("line %d: unterminated subsection quote", lineNo)
		}
		var b strings.Builder
		inner := raw[1 : len(raw)-1]
		for j := 0; j < len(inner); j++ {
			if inner[j] == '\\' && j+1 < len(inner) {
				j++ // \" and \\ unescape to the literal char; git ignores other escapes here
			}
			b.WriteByte(inner[j])
		}
		sub = b.String()
	}
	if name == "" || !validSectionName(name) {
		return "", "", fmt.Errorf("line %d: invalid section name %q", lineNo, name)
	}
	return strings.ToLower(name), sub, nil
}

func validSectionName(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// splitKey splits `key = value` (or a bare `key`, which git reads as true).
func splitKey(line string, lineNo int) (key, rest string, err error) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		key = strings.TrimSpace(line)
		rest = "true"
	} else {
		key = strings.TrimSpace(line[:eq])
		rest = strings.TrimLeft(line[eq+1:], " \t")
	}
	if key == "" {
		return "", "", fmt.Errorf("line %d: empty key", lineNo)
	}
	for i, c := range key {
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !(alpha || c == '-' || (i > 0 && c >= '0' && c <= '9')) {
			return "", "", fmt.Errorf("line %d: invalid key name %q", lineNo, key)
		}
	}
	return strings.ToLower(key), rest, nil
}

// parseValue scans a value, honoring quotes, escapes, comments outside
// quotes, and `\` line continuations. It returns how many extra source
// lines it consumed.
func parseValue(rest string, following []string, lineNo int) (string, int, error) {
	var b strings.Builder
	inQuote := false
	consumed := 0
	spaceRun := 0 // pending unquoted trailing whitespace, flushed lazily
	flush := func() {
		for ; spaceRun > 0; spaceRun-- {
			b.WriteByte(' ')
		}
	}
	line := rest
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\':
			if i == len(line)-1 { // continuation: value flows onto the next line
				if consumed >= len(following) {
					return "", 0, fmt.Errorf("line %d: line continuation at end of file", lineNo+consumed)
				}
				line = following[consumed]
				consumed++
				i = -1
				continue
			}
			i++
			flush()
			switch line[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			case '"', '\\':
				b.WriteByte(line[i])
			default:
				return "", 0, fmt.Errorf("line %d: invalid escape \\%c", lineNo+consumed, line[i])
			}
		case c == '"':
			inQuote = !inQuote
			flush()
		case !inQuote && (c == '#' || c == ';'):
			return b.String(), consumed, nil // comment ends the value; trailing spaces stay dropped
		case !inQuote && (c == ' ' || c == '\t'):
			spaceRun++
		default:
			flush()
			b.WriteByte(c)
		}
	}
	if inQuote {
		return "", 0, fmt.Errorf("line %d: unterminated quoted value", lineNo+consumed)
	}
	return b.String(), consumed, nil
}

// Modules extracts submodule blocks from parsed entries. Later values win,
// matching git's precedence. Modules without a path are dropped (git cannot
// map them to a worktree either). The result is sorted by path.
func Modules(entries []Entry) []Module {
	byName := map[string]*Module{}
	var order []string
	for _, e := range entries {
		if e.Section != "submodule" || e.Subsection == "" {
			continue
		}
		m, ok := byName[e.Subsection]
		if !ok {
			m = &Module{Name: e.Subsection}
			byName[e.Subsection] = m
			order = append(order, e.Subsection)
		}
		switch e.Key {
		case "path":
			m.Path = e.Value
		case "url":
			m.URL = e.Value
		case "branch":
			m.Branch = e.Value
		}
	}
	var out []Module
	for _, name := range order {
		if m := byName[name]; m.Path != "" {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
