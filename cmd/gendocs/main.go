package main

import "fmt"

// TODO(docs-generation): read the cobra command tree from cmd/nesdit
// and emit manpages + markdown reference under docs/reference/ via
// cobra/doc. STORY-0003 delivered the cobra tree but the docs
// generator itself is owned by a future docs-focused story (the
// Makefile `docs` target and `.github/workflows/docs.yml` already
// invoke this binary, so it remains a no-op stub rather than being
// deleted — once the docs story is filed, replace this body with the
// real generator and drop this TODO).
func main() {
	fmt.Println("gendocs: placeholder; cobra tree landed in STORY-0003, real generator pending a docs-focused story")
}
