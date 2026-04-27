// Package main generates CLI reference documentation from the nesdit cobra
// command tree and writes the output as markdown files.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cobradoc "github.com/spf13/cobra/doc"

	"github.com/sdkks/nesdit/internal/run"
)

func main() {
	outDir := flag.String("out", "docs/reference", "output directory for generated markdown")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "gendocs: mkdir %s: %v\n", *outDir, err)
		os.Exit(1)
	}

	root := run.NewRootCmd()
	// Suppress the auto-generated completion subcommand from docs.
	root.CompletionOptions.DisableDefaultCmd = true

	filePrepender := func(_ string) string { return "" }
	// linkHandler rewrites cross-doc links to relative .md paths for mkdocs.
	linkHandler := func(ref string) string {
		return strings.TrimSuffix(filepath.Base(ref), ".md") + ".md"
	}

	if err := cobradoc.GenMarkdownTreeCustom(root, *outDir, filePrepender, linkHandler); err != nil {
		fmt.Fprintf(os.Stderr, "gendocs: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("gendocs: written to %s\n", *outDir)
}
