package main

import (
	"os"

	"github.com/sakkas-zendesk/nesdit/internal/run"
)

func main() {
	os.Exit(run.Run(os.Args[1:]))
}
