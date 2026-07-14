// Unit tests for scan's pure parsing helpers and for git's
// relative-submodule-URL resolution rules. The orchestration path (real
// repositories) is covered by the CLI integration tests.
package scan

import (
	"testing"
)

func TestParseGitlinksKeepsOnlyMode160000(t *testing.T) {
	// Regular files (100644/100755) must be ignored; gitlink paths may
	// contain spaces; empty input yields no entries.
	out := "100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 0\tREADME.md\x00" +
		"160000 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 0\tlibs/dep\x00" +
		"160000 dddddddddddddddddddddddddddddddddddddddd 0\tvendor/my lib\x00" +
		"100755 cccccccccccccccccccccccccccccccccccccccc 0\tscripts/run.sh\x00"
	links := ParseGitlinks(out)
	if len(links) != 2 {
		t.Fatalf("want exactly the gitlink entries, got %v", links)
	}
	if links["libs/dep"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("wrong sha: %v", links)
	}
	if links["vendor/my lib"] == "" {
		t.Fatalf("path with spaces lost: %v", links)
	}
	if empty := ParseGitlinks(""); len(empty) != 0 {
		t.Fatalf("empty input should yield no links, got %v", empty)
	}
}

func TestParseConfigURLs(t *testing.T) {
	out := "submodule.libs/dep.url https://example.test/dep.git\n" +
		"submodule.with.dots.url https://example.test/dots.git"
	urls := ParseConfigURLs(out)
	if urls["libs/dep"] != "https://example.test/dep.git" {
		t.Fatalf("simple name wrong: %v", urls)
	}
	// Submodule names may contain dots; only the fixed prefix/suffix are cut.
	if urls["with.dots"] != "https://example.test/dots.git" {
		t.Fatalf("dotted name wrong: %v", urls)
	}
}

func TestParseConfigURLsIgnoresForeignKeys(t *testing.T) {
	urls := ParseConfigURLs("submodule.a.active true\ncore.bare false")
	if len(urls) != 0 {
		t.Fatalf("non-url keys leaked: %v", urls)
	}
}

func TestParseStatusClassifiesDirtyAndUntracked(t *testing.T) {
	cases := []struct {
		out              string
		dirty, untracked bool
	}{
		{"", false, false},
		{" M lib.txt", true, false},
		{"?? new.txt", false, true},
		{"M  staged.txt\n?? new.txt", true, true},
		{"D  gone.txt", true, false},
	}
	for _, c := range cases {
		dirty, untracked := ParseStatus(c.out)
		if dirty != c.dirty || untracked != c.untracked {
			t.Errorf("ParseStatus(%q) = (%v,%v), want (%v,%v)",
				c.out, dirty, untracked, c.dirty, c.untracked)
		}
	}
}

func TestResolveRelativeMatchesGitRules(t *testing.T) {
	// Mirrors gitsubmodules(7): `../` strips one segment from the origin,
	// `./` strips nothing, scp-like and URL bases never lose their host.
	cases := []struct{ name, base, rel, want string }{
		{"https single parent", "https://example.test/group/super.git", "../dep.git",
			"https://example.test/group/dep.git"},
		{"https double parent", "https://example.test/org/group/super.git", "../../other/dep.git",
			"https://example.test/org/other/dep.git"},
		{"dot-slash appends", "https://example.test/group/super", "./dep.git",
			"https://example.test/group/super/dep.git"},
		// ../../.. beyond the path depth must stop at the host, not corrupt it.
		{"scp syntax never eats host", "git@example.test:group/super.git", "../../../dep.git",
			"git@example.test:dep.git"},
		{"plain filesystem path", "/srv/git/group/super", "../dep",
			"/srv/git/group/dep"},
		{"trailing slash normalized", "https://example.test/group/super/", "../dep.git",
			"https://example.test/group/dep.git"},
	}
	for _, c := range cases {
		if got := ResolveRelative(c.base, c.rel); got != c.want {
			t.Errorf("%s: ResolveRelative(%q, %q) = %q, want %q", c.name, c.base, c.rel, got, c.want)
		}
	}
}

func TestResolveURLAbsolutePassesThrough(t *testing.T) {
	resolved, unresolvable := resolveURL("https://example.test/dep.git", "")
	if resolved != "https://example.test/dep.git" || unresolvable {
		t.Fatalf("absolute URL must pass through untouched: %q %v", resolved, unresolvable)
	}
}

func TestResolveURLRelativeWithoutOriginIsUnresolvable(t *testing.T) {
	// Without an origin remote there is nothing to resolve against; the
	// URL-drift check must stand down rather than report a false positive.
	_, unresolvable := resolveURL("../dep.git", "")
	if !unresolvable {
		t.Fatal("relative URL with no origin must be flagged unresolvable")
	}
}
