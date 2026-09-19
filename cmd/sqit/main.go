// Command sqit is a Git-like version control and synchronisation tool for
// MySQL data. See DESIGN.md.
package main

import (
	"os"

	"sqit/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
