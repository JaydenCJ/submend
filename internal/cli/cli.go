// Package cli implements the submend command-line interface. Run takes argv
// and two writers and returns an exit code, so the whole surface is testable
// in-process without building a binary.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/JaydenCJ/submend/internal/gitio"
	"github.com/JaydenCJ/submend/internal/version"
)

// Exit codes, documented in the README. `doctor` uses ExitFindings as its
// machine-readable verdict, like a linter.
const (
	ExitOK       = 0
	ExitFindings = 1
	ExitUsage    = 2
	ExitRuntime  = 3
)

// Run dispatches argv and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	runner := gitio.System{}
	if len(args) == 0 {
		return runDoctor(runner, nil, stdout, stderr)
	}
	switch args[0] {
	case "doctor":
		return runDoctor(runner, args[1:], stdout, stderr)
	case "fix":
		return runFix(runner, args[1:], stdout, stderr)
	case "undo":
		return runUndo(runner, args[1:], stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "submend %s\n", version.Version)
		return ExitOK
	case "help", "--help", "-h":
		usage(stdout)
		return ExitOK
	default:
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(stderr, "submend: unknown flag %q before a subcommand\n\n", args[0])
			usage(stderr)
			return ExitUsage
		}
		// Bare path: treat as `doctor <path>`.
		return runDoctor(runner, args, stdout, stderr)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `submend — git submodule doctor with explained, reversible fixes

Usage:
  submend [doctor] [flags] [repo]   diagnose every submodule (default command)
  submend fix [flags] [repo]        apply safe fixes; writes an undo journal
  submend undo [flags] [repo]       revert the last fix session from the journal
  submend explain [ID]              document one check (or all of them)
  submend version                   print the version

Flags (doctor):
  --format text|json     output format (default text)

Flags (fix):
  --format text|json     output format (default text)
  --dry-run              plan and print, change nothing
  --only ID[,ID...]      restrict to specific checks, e.g. --only SM02,SM04

Flags (undo):
  --dry-run              show what would be reverted, change nothing

Exit codes:
  0 healthy / done   1 warnings or errors found   2 usage error   3 runtime error
`)
}

// newFlagSet builds a silent FlagSet whose errors we render ourselves.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	_ = stderr
	return fs
}

// repoArg extracts the optional trailing repository path (default ".").
func repoArg(fs *flag.FlagSet, stderr io.Writer) (string, bool) {
	switch fs.NArg() {
	case 0:
		return ".", true
	case 1:
		return fs.Arg(0), true
	default:
		fmt.Fprintf(stderr, "submend: expected at most one repository path, got %d\n", fs.NArg())
		return "", false
	}
}

func usageErr(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "submend: %v\n", err)
	return ExitUsage
}

func runtimeErr(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "submend: %v\n", err)
	return ExitRuntime
}

func validFormat(f string) bool { return f == "text" || f == "json" }
