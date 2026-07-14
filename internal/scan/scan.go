// Package scan gathers the raw state of every submodule in a superproject:
// what .gitmodules declares, what .git/config has, what the index records
// (the gitlink), and what is actually checked out. It only observes — the
// diagnosis lives in package checks and mutation in package fixes.
package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JaydenCJ/submend/internal/gitconf"
	"github.com/JaydenCJ/submend/internal/gitio"
)

// State is everything we know about one submodule (or one orphan gitlink).
type State struct {
	Name string // .gitmodules subsection name; "" for orphan gitlinks
	Path string // path relative to the superproject root

	// Declared configuration.
	InGitmodules bool
	URLModules   string // .gitmodules URL after relative resolution
	URLRaw       string // .gitmodules URL exactly as written
	Unresolvable bool   // relative URL but the superproject has no origin
	Branch       string // .gitmodules branch, if any

	// Superproject-side state.
	HasGitlink  bool
	GitlinkSHA  string
	Initialized bool // submodule.<name>.url present in .git/config
	URLConfig   string

	// Worktree-side state (zero values when not cloned).
	Cloned         bool
	EmbeddedGit    bool // .git is a directory instead of a gitfile
	HeadSHA        string
	HeadBranch     string // "" when detached
	URLRemote      string
	RemoteMissing  bool // cloned but no origin remote at all
	Dirty          bool
	Untracked      bool
	GitlinkInSub   bool     // the recorded commit exists in the submodule's object store
	Ahead          int      // commits on HEAD not reachable from the gitlink (-1 unknown)
	Behind         int      // commits on the gitlink not reachable from HEAD (-1 unknown)
	BranchesAtHead []string // local branches pointing exactly at HEAD
	HeadOnRef      bool     // HEAD reachable from any local or remote branch
	RescueExists   bool     // refs/heads/submend-rescue already exists
}

// Result is a full scan of one superproject.
type Result struct {
	Root      string // absolute worktree root
	GitDir    string // absolute .git directory
	HeadDesc  string // e.g. "main @ 1a2b3c4"
	OriginURL string
	Subs      []State // sorted by path
}

// Scan inspects the repository at repo (any directory inside it).
func Scan(r gitio.Runner, repo string) (*Result, error) {
	root, err := r.Run(repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git repository", repo)
	}
	gitDir, err := r.Run(repo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	res := &Result{Root: root, GitDir: gitDir}
	res.OriginURL, _ = r.Run(root, "config", "--get", "remote.origin.url")
	res.HeadDesc = headDesc(r, root)

	modules, err := readGitmodules(root)
	if err != nil {
		return nil, err
	}
	gitlinks, err := gitlinkMap(r, root)
	if err != nil {
		return nil, err
	}
	configURLs := configURLMap(r, root)

	byPath := map[string]*State{}
	var order []string
	get := func(path string) *State {
		if s, ok := byPath[path]; ok {
			return s
		}
		s := &State{Path: path, Ahead: -1, Behind: -1}
		byPath[path] = s
		order = append(order, path)
		return s
	}
	for _, m := range modules {
		s := get(m.Path)
		s.Name = m.Name
		s.InGitmodules = true
		s.URLRaw = m.URL
		s.Branch = m.Branch
		s.URLModules, s.Unresolvable = resolveURL(m.URL, res.OriginURL)
		if url, ok := configURLs[m.Name]; ok {
			s.Initialized = true
			s.URLConfig = url
		}
	}
	for path, sha := range gitlinks {
		s := get(path)
		s.HasGitlink = true
		s.GitlinkSHA = sha
	}
	for _, path := range order {
		inspectWorktree(r, root, byPath[path])
	}

	sort.Strings(order)
	for _, path := range order {
		res.Subs = append(res.Subs, *byPath[path])
	}
	return res, nil
}

func headDesc(r gitio.Runner, root string) string {
	sha, err := r.Run(root, "rev-parse", "--verify", "--short", "HEAD")
	if err != nil {
		return "no commits yet"
	}
	if branch, err := r.Run(root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && branch != "" {
		return branch + " @ " + sha
	}
	return "detached @ " + sha
}

func readGitmodules(root string) ([]gitconf.Module, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gitmodules"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := gitconf.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf(".gitmodules: %w", err)
	}
	return gitconf.Modules(entries), nil
}

// inspectWorktree fills in the clone-side fields of s.
func inspectWorktree(r gitio.Runner, root string, s *State) {
	sub := filepath.Join(root, s.Path)
	info, err := os.Stat(filepath.Join(sub, ".git"))
	if err != nil {
		return // not cloned (or path missing entirely)
	}
	s.Cloned = true
	s.EmbeddedGit = info.IsDir()

	if sha, err := r.Run(sub, "rev-parse", "--verify", "HEAD"); err == nil {
		s.HeadSHA = sha
	}
	if branch, err := r.Run(sub, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		s.HeadBranch = branch
	}
	if url, err := r.Run(sub, "remote", "get-url", "origin"); err == nil {
		s.URLRemote = url
	} else {
		s.RemoteMissing = true
	}
	if status, err := r.Run(sub, "status", "--porcelain", "--untracked-files=normal"); err == nil {
		s.Dirty, s.Untracked = ParseStatus(status)
	}
	if s.GitlinkSHA != "" {
		if _, err := r.Run(sub, "cat-file", "-e", s.GitlinkSHA+"^{commit}"); err == nil {
			s.GitlinkInSub = true
		}
	}
	if s.GitlinkInSub && s.HeadSHA != "" && s.HeadSHA != s.GitlinkSHA {
		s.Ahead = revCount(r, sub, s.GitlinkSHA+".."+s.HeadSHA)
		s.Behind = revCount(r, sub, s.HeadSHA+".."+s.GitlinkSHA)
	}
	if s.HeadSHA != "" {
		if out, err := r.Run(sub, "for-each-ref", "--points-at", "HEAD",
			"--format=%(refname:short)", "refs/heads"); err == nil && out != "" {
			s.BranchesAtHead = strings.Split(out, "\n")
			sort.Strings(s.BranchesAtHead)
		}
		if out, err := r.Run(sub, "for-each-ref", "--contains", "HEAD",
			"--format=%(refname:short)", "refs/heads", "refs/remotes"); err == nil && out != "" {
			s.HeadOnRef = true
		}
	}
	if _, err := r.Run(sub, "show-ref", "--verify", "--quiet", "refs/heads/submend-rescue"); err == nil {
		s.RescueExists = true
	}
}

func revCount(r gitio.Runner, dir, spec string) int {
	out, err := r.Run(dir, "rev-list", "--count", spec)
	if err != nil {
		return -1
	}
	n := 0
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		return -1
	}
	return n
}

// gitlinkMap reads mode-160000 index entries: path -> recorded commit SHA.
func gitlinkMap(r gitio.Runner, root string) (map[string]string, error) {
	out, err := r.Run(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	return ParseGitlinks(out), nil
}

// ParseGitlinks parses `git ls-files --stage -z` output, keeping only
// gitlink (mode 160000) entries. Exported for direct unit testing.
func ParseGitlinks(out string) map[string]string {
	links := map[string]string{}
	for _, rec := range strings.Split(out, "\x00") {
		// <mode> <sha> <stage>\t<path>
		if !strings.HasPrefix(rec, "160000 ") {
			continue
		}
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(rec[:tab])
		if len(fields) < 3 {
			continue
		}
		links[rec[tab+1:]] = fields[1]
	}
	return links
}

// configURLMap reads submodule.<name>.url from the superproject's local
// config: name -> URL. A missing section is not an error (exit 1).
func configURLMap(r gitio.Runner, root string) map[string]string {
	out, err := r.Run(root, "config", "--local", "--get-regexp", `^submodule\..*\.url$`)
	if err != nil {
		return map[string]string{}
	}
	return ParseConfigURLs(out)
}

// ParseConfigURLs parses `git config --get-regexp ^submodule\..*\.url$`
// lines. Submodule names may themselves contain dots, so the name is
// everything between the fixed prefix and suffix.
func ParseConfigURLs(out string) map[string]string {
	urls := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		name := strings.TrimPrefix(key, "submodule.")
		if name == key || !strings.HasSuffix(name, ".url") {
			continue
		}
		urls[strings.TrimSuffix(name, ".url")] = value
	}
	return urls
}

// ParseStatus interprets `git status --porcelain` output.
func ParseStatus(out string) (dirty, untracked bool) {
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 2 {
			continue
		}
		if strings.HasPrefix(line, "??") {
			untracked = true
		} else {
			dirty = true
		}
	}
	return dirty, untracked
}

// resolveURL turns a .gitmodules URL into the effective URL git would
// configure. Relative URLs (./x, ../x) resolve against the superproject's
// origin; when there is no origin the URL is reported unresolvable and URL
// drift checks stand down rather than false-positive.
func resolveURL(url, origin string) (resolved string, unresolvable bool) {
	if !strings.HasPrefix(url, "./") && !strings.HasPrefix(url, "../") {
		return url, false
	}
	if origin == "" {
		return url, true
	}
	return ResolveRelative(origin, url), false
}

// ResolveRelative applies git's relative-submodule-URL rules: each leading
// `../` strips one path segment from the base, `./` strips nothing, and the
// remainder is joined with `/`. Handles plain paths, URL syntax, and
// scp-like `host:path` syntax.
func ResolveRelative(base, rel string) string {
	base = strings.TrimRight(base, "/")
	// Split an scp-like base (git@host:path) so we never strip into the host.
	prefix := ""
	if i := strings.Index(base, "://"); i >= 0 {
		if j := strings.IndexByte(base[i+3:], '/'); j >= 0 {
			prefix, base = base[:i+3+j], base[i+3+j:]
		} else {
			prefix, base = base, ""
		}
	} else if i := strings.IndexByte(base, ':'); i >= 0 {
		prefix, base = base[:i+1], base[i+1:]
	}
	for {
		if strings.HasPrefix(rel, "./") {
			rel = rel[2:]
			continue
		}
		if strings.HasPrefix(rel, "../") {
			rel = rel[3:]
			if j := strings.LastIndexByte(base, '/'); j >= 0 {
				base = base[:j]
			} else {
				base = ""
			}
			continue
		}
		break
	}
	switch {
	case base == "":
		return prefix + rel
	case rel == "":
		return prefix + base
	default:
		return prefix + base + "/" + rel
	}
}
