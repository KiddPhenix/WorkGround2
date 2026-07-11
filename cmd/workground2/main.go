// Command WorkGround2 is a config- and plugin-driven coding agent CLI.
package main

import (
	"os"

	"workground2/internal/cli"

	// Blank imports wire compile-time built-ins into their registries.
	_ "workground2/internal/provider/anthropic"
	_ "workground2/internal/provider/cli"
	_ "workground2/internal/provider/openai"
	_ "workground2/internal/tool/builtin"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
