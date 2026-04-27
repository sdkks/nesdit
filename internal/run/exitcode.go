package run

// ExitCode is a typed integer enum for the process exit codes produced by
// nesdit. Using a named type rather than bare int literals ensures the
// compiler can enforce exhaustiveness checks at switch sites, and it
// makes the DR-002 constraint ("exit 2 may only originate from the
// --check path") statically visible in the type system.
//
// The three values correspond to FR-20 / DR-002:
//   - ExitOK (0)     — success, no drift.
//   - ExitError (1)  — any error (parse, query, encode, IO, flag, etc.).
//   - ExitDrift (2)  — --check detected that the query would change the input.
//
// DR-002 lock-in: ExitDrift MUST NOT be returned from any code path other
// than the --check implementation in runCheck. The unit test
// Test_ExitCode_2_Only_From_Check_Path enforces this statically by
// asserting that no caller of Execute or Run returns the integer literal 2
// other than through runCheck.
type ExitCode int

const (
	// ExitOK is the success exit code (0). Used for successful edits,
	// successful --dry-run runs, and --check with no drift.
	ExitOK ExitCode = 0

	// ExitError is the generic error exit code (1). Used for every
	// non-success that is not drift detection: parse failure, query
	// error, encode failure, IO failure, flag errors, etc.
	ExitError ExitCode = 1

	// ExitDrift is the drift detection exit code (2). Used exclusively
	// by the --check path when the query would change the input.
	// DR-002: this value MUST NOT be produced by any other path.
	ExitDrift ExitCode = 2
)
