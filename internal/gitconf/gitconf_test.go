// Unit tests for the pure git-config parser. Each case mirrors a shape git
// itself accepts in hand-written .gitmodules files, so the parser and git
// never disagree on real-world input.
package gitconf

import (
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) []Entry {
	t.Helper()
	entries, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", src, err)
	}
	return entries
}

func TestParseSimpleSubmoduleBlock(t *testing.T) {
	entries := mustParse(t, `[submodule "libs/dep"]
	path = libs/dep
	url = https://example.test/dep.git
`)
	want := []Entry{
		{"submodule", "libs/dep", "path", "libs/dep"},
		{"submodule", "libs/dep", "url", "https://example.test/dep.git"},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("got %+v, want %+v", entries, want)
	}
}

func TestParseSectionAndKeyAreCaseInsensitive(t *testing.T) {
	entries := mustParse(t, "[SubModule \"Dep\"]\n\tURL = x\n")
	if entries[0].Section != "submodule" || entries[0].Key != "url" {
		t.Fatalf("section/key not lower-cased: %+v", entries[0])
	}
	// The subsection is case-SENSITIVE in git; the exact case must survive.
	if entries[0].Subsection != "Dep" {
		t.Fatalf("subsection case not preserved: %q", entries[0].Subsection)
	}
}

func TestParseCommentsAndBlankLinesIgnored(t *testing.T) {
	entries := mustParse(t, `# leading comment
; alt comment

[submodule "a"]
	# inside section
	path = a   ; trailing comment
`)
	if len(entries) != 1 || entries[0].Value != "a" {
		t.Fatalf("comment handling wrong: %+v", entries)
	}
}

func TestParseValueQuotingEscapesAndContinuation(t *testing.T) {
	// Value-lexing rules from git-config(1), one table row per rule.
	cases := []struct{ name, src, want string }{
		{"quotes shield comment chars", "[a]\nkey = \"v ; not a comment # either\"\n",
			"v ; not a comment # either"},
		{"escapes", "[a]\nkey = a\\\"b\\\\c\\td\n", "a\"b\\c\td"},
		{"backslash continuation joins lines", "[a]\nkey = one\\\ntwo\n", "onetwo"},
		{"unquoted trailing whitespace dropped", "[a]\nkey = value   \n", "value"},
		{"internal whitespace preserved", "[a]\nkey = two  words\n", "two  words"},
	}
	for _, c := range cases {
		if got := mustParse(t, c.src)[0].Value; got != c.want {
			t.Errorf("%s: value = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseBareKeyReadsAsTrue(t *testing.T) {
	entries := mustParse(t, "[a]\nflag\n")
	if entries[0].Key != "flag" || entries[0].Value != "true" {
		t.Fatalf("bare key wrong: %+v", entries[0])
	}
}

func TestParseEscapedQuoteInSubsection(t *testing.T) {
	entries := mustParse(t, "[submodule \"we\\\"ird\"]\npath = p\n")
	if entries[0].Subsection != `we"ird` {
		t.Fatalf("escaped subsection wrong: %q", entries[0].Subsection)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"key before section":  "key = v\n",
		"unterminated header": "[submodule \"x\"\npath = p\n",
		"unterminated quote":  "[a]\nkey = \"open\n",
		"invalid escape":      "[a]\nkey = \\x\n",
		"invalid key char":    "[a]\nbad_key = v\n", // underscores are not valid in git keys
		"key starts digit":    "[a]\n1key = v\n",
		"invalid section":     "[a b]\nk = v\n",
		"continuation at EOF": "[a]\nkey = v\\",
	}
	for name, src := range cases {
		if _, err := Parse(src); err == nil {
			t.Errorf("%s: Parse(%q) should fail", name, src)
		} else if !strings.Contains(err.Error(), "line ") {
			t.Errorf("%s: error should carry a line number, got %v", name, err)
		}
	}
}

func TestModulesExtractsAndSortsByPath(t *testing.T) {
	entries := mustParse(t, `[submodule "z"]
	path = vendor/z
	url = ../z.git
[submodule "a"]
	path = libs/a
	url = ../a.git
	branch = stable
`)
	mods := Modules(entries)
	if len(mods) != 2 {
		t.Fatalf("want 2 modules, got %+v", mods)
	}
	if mods[0].Path != "libs/a" || mods[1].Path != "vendor/z" {
		t.Fatalf("not sorted by path: %+v", mods)
	}
	if mods[0].Branch != "stable" || mods[1].Branch != "" {
		t.Fatalf("branch extraction wrong: %+v", mods)
	}
}

func TestModulesLastValueWins(t *testing.T) {
	entries := mustParse(t, `[submodule "a"]
	path = one
	url = first
[submodule "a"]
	url = second
`)
	mods := Modules(entries)
	if len(mods) != 1 || mods[0].URL != "second" || mods[0].Path != "one" {
		t.Fatalf("git precedence (last wins) violated: %+v", mods)
	}
}

func TestModulesDropsEntriesWithoutPath(t *testing.T) {
	entries := mustParse(t, "[submodule \"ghost\"]\nurl = somewhere\n")
	if mods := Modules(entries); len(mods) != 0 {
		t.Fatalf("pathless module should be dropped, got %+v", mods)
	}
}

func TestModulesIgnoresOtherSections(t *testing.T) {
	entries := mustParse(t, "[core]\nbare = false\n[submodule \"a\"]\npath = a\nurl = u\n")
	mods := Modules(entries)
	if len(mods) != 1 || mods[0].Name != "a" {
		t.Fatalf("non-submodule sections leaked: %+v", mods)
	}
}

func TestParseEmptyInput(t *testing.T) {
	if entries := mustParse(t, ""); len(entries) != 0 {
		t.Fatalf("empty input should parse to no entries, got %+v", entries)
	}
}
