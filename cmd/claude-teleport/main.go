package main

import (
	"os"

	"github.com/mithro/go-claude-teleport/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
}
