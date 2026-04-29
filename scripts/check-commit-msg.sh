#!/usr/bin/env bash
# Enforce Angular/semantic-release commit message format on the first line.
# Allowed types match the default @semantic-release/commit-analyzer preset.
# Pattern: <type>[optional scope][optional !]: <description>

set -eo pipefail

MSG_FILE="${1:-.git/COMMIT_EDITMSG}"
FIRST_LINE="$(head -1 "${MSG_FILE}")"

# Skip merge commits and fixup/squash commits (rebase internals).
case "${FIRST_LINE}" in
    Merge\ *|fixup\!*|squash\!*) exit 0 ;;
esac

TYPES="feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert"
PATTERN="^(${TYPES})(\([^)]+\))?(!)?: .+"

if ! echo "${FIRST_LINE}" | grep -qE "${PATTERN}"; then
    echo ""
    echo "  commit-msg: first line does not match semantic-release format."
    echo ""
    echo "  Expected:  <type>[(<scope>)][!]: <description>"
    echo "  Got:       ${FIRST_LINE}"
    echo ""
    echo "  Valid types: feat fix docs style refactor perf test build ci chore revert"
    echo "  Examples:"
    echo "    feat: add --dry-run flag"
    echo "    fix(yaml): preserve quoted strings on round-trip"
    echo "    feat!: drop support for Go 1.21 (breaking change)"
    echo ""
    exit 1
fi
