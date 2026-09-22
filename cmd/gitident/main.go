// Command gitident keeps git identities in one profiles.yaml and renders them
// into includeIf rules in the global gitconfig.
package main

import (
	"os"

	"github.com/leoru/gitident-cli/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.New(version).Run(os.Args[1:]))
}
