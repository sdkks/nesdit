// Package main is the process entry point for the nesdit CLI.
package main

import (
	"os"

	"github.com/sdkks/nesdit/internal/run"
)

func main() {
	os.Exit(run.Run(os.Args[1:]))
}
