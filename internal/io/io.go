// Package nesio implements atomic writes and backups. It is named "nesio"
// rather than "io" to avoid shadowing the stdlib "io" package at import sites.
package nesio

// Atomic tempfile+rename writes, symlink resolution, and sibling backups
// are implemented in atomic.go.
