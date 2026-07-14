// Package gitio is the only place submend talks to the outside world: it
// shells out to the local `git` binary. Everything above it is pure and
// unit-testable; tests substitute the Runner interface with a fake.
package gitio

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes a git command in dir and returns its stdout. Implementers
// must return an error that carries the exit code (see ExitCode) when git
// exits non-zero.
type Runner interface {
	Run(dir string, args ...string) (string, error)
}

// System is the production Runner backed by os/exec.
type System struct{}

// Run executes `git <args...>` in dir and returns trimmed stdout. On a
// non-zero exit the error wraps the exit code and the trimmed stderr text so
// callers can both branch on the code and surface a useful message.
func (System) Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	stdout := strings.TrimRight(out.String(), "\n")
	if err != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout, &Error{Args: args, Code: code, Stderr: msg}
	}
	return stdout, nil
}

// Error is a failed git invocation.
type Error struct {
	Args   []string
	Code   int
	Stderr string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.Code, e.Stderr)
}

// ExitCode extracts the git exit code from an error produced by a Runner,
// or -1 when the error is not a git exit (e.g. binary missing).
func ExitCode(err error) int {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Code
	}
	return -1
}
