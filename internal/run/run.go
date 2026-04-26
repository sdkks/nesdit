// Package run hosts the nesdit CLI entrypoint. It is deliberately a
// non-main package so both cmd/nesdit and the e2e testscript harness
// (test/e2e/main_test.go) can call Run without the `package main` import
// restriction. TASK-0002 reserved this package for the top-level orchestrator
// and the DR-002 ExitCode enum; TASK-0003 introduces the Run shim only.
//
// TODO(STORY-0003): replace the stub Run body with the cobra root command
// tree and define the ExitCode typed enum here per SPEC-0001 §5.
package run

// Run is the nesdit CLI entrypoint. Accepts the raw argv tail
// (os.Args[1:]) and returns the process exit code.
func Run(args []string) int {
	_ = args
	return 0
}
