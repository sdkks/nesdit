// Package run hosts the nesdit CLI entrypoint. It is deliberately a
// non-main package so both cmd/nesdit and the e2e testscript harness can
// call into it without the `package main` import restriction. Reserved for
// the top-level orchestrator and the DR-002 ExitCode enum in a later story.
package run

// TODO(STORY-0003): define Run(args []string) int plus the ExitCode typed
// enum per SPEC-0001 §5.
