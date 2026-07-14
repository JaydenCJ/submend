// Command submend diagnoses git submodule problems — detached heads, URL
// drift, dirty states — and applies explained, reversible fixes.
package main

import (
	"os"

	"github.com/JaydenCJ/submend/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
