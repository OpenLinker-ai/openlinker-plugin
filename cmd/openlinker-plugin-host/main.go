package main

import (
	"github.com/OpenLinker-ai/openlinker-plugin/internal/pluginhost/root"
	"os"
)

func main() {
	os.Exit(root.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
