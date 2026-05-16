// Command superkube is a safer, prettier, AI-assisted wrapper around kubectl.
//
// The binary is installed as `superkube` with `sk` as a convenience symlink.
// See https://github.com/optimumsage/superkube for documentation.
package main

import (
	"os"

	"github.com/optimumsage/superkube/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
