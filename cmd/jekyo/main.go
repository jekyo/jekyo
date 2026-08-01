package main

import (
	"fmt"
	"os"
)

// set via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
