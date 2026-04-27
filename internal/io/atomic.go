// Package nesio implements atomic writes for nesdit's in-place (-i) mode.
//
// The atomic writer uses the classic temp-file + rename pattern so a crash
// or power loss between step 4 and step 5 of the two-pass orchestrator
// (NFR-7) leaves either the original bytes or the complete new bytes on
// disk — never a mixed/partial file.
//
// Symlink resolution: when the caller supplies a symlink path, WriteAtomic
// resolves it via filepath.EvalSymlinks before writing, so the underlying
// target file receives the new bytes and the symlink is not replaced.
package nesio

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteAtomic writes data to dest atomically using a temp file + rename.
//
// If dest is a symlink, it is resolved to its ultimate target via
// filepath.EvalSymlinks before writing. The symlink itself is not replaced —
// only the target file is updated.
//
// The temp file is created in the same directory as the resolved destination
// so that the final os.Rename is guaranteed to be an atomic same-filesystem
// rename (cross-device rename fails on most OSes; same-dir placement avoids
// it). Permissions on the temp file are set to match the original file before
// the rename so the effective permissions do not change.
//
// On failure, the temp file is removed and the original is unchanged.
func WriteAtomic(dest string, data []byte) error {
	// Resolve symlinks so we write to the underlying target, not a new regular
	// file at the symlink path. filepath.EvalSymlinks returns the cleaned real
	// path; if dest is not a symlink it returns dest unchanged (cleaned).
	target, err := resolveTarget(dest)
	if err != nil {
		return fmt.Errorf("resolve target %s: %w", dest, err)
	}

	// Capture original file permissions so we can restore them on the temp
	// before rename. If the file does not yet exist (new file creation path),
	// we fall back to 0644.
	perm := os.FileMode(0o644)
	if fi, statErr := os.Stat(target); statErr == nil {
		perm = fi.Mode().Perm()
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".nesdit-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Ensure the temp file is removed on any failure path.
	// On the success path we rename it away, so os.Remove is a no-op there.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmpName, err)
	}

	// Set permissions to match the original BEFORE rename so the file is
	// never transiently world-writable or inaccessible.
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod temp file %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmpName, target, err)
	}
	cleanup = false // rename succeeded; nothing to clean up
	return nil
}

// resolveTarget resolves dest to its final target path following symlinks.
// If dest does not exist yet, filepath.EvalSymlinks will return an error;
// in that case we return the clean form of dest as a new-file path.
func resolveTarget(dest string) (string, error) {
	// filepath.EvalSymlinks requires the path to exist. We need to handle
	// the new-file creation case gracefully.
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		// If the file doesn't exist, fall through to using the raw path.
		if os.IsNotExist(err) {
			return filepath.Clean(dest), nil
		}
		return "", err
	}
	return resolved, nil
}

// DeduplicatePaths returns a new slice containing only the first occurrence
// of each path, using the resolved (EvalSymlinks) form as the deduplication
// key. Paths that cannot be resolved (do not exist yet or are unresolvable)
// are included as-is and deduplicated by their cleaned form.
//
// This ensures that when a glob expands to both a symlink and its target,
// or two symlinks pointing to the same file, only one write happens.
func DeduplicatePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		key, err := filepath.EvalSymlinks(p)
		if err != nil {
			key = filepath.Clean(p)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}
