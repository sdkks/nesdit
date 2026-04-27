#!/usr/bin/env bash
# check-action-pins.sh — SHA↔tag consistency guard for GitHub Actions pins.
#
# Usage:
#   scripts/check-action-pins.sh [workflow-files...]
#
# Defaults to .github/workflows/*.yml when no files are supplied.
#
# For every `uses: owner/repo@<sha>  # <tag>` line found in the supplied
# workflow files, this script:
#   1. Resolves the commented tag to a commit SHA via the GitHub API.
#      Annotated tags are dereferenced (tag-object → commit).
#   2. Compares that resolved SHA to the pinned SHA in the `uses:` line.
#   3. Exits 1 with a clear diff summary when any mismatch is found.
#
# Requirements:
#   - gh (GitHub CLI) authenticated with at least public-repo read scope.
#   - grep, awk, sed (POSIX-compatible).
#
# Environment variables:
#   GH_TOKEN   — optional; passed through by `gh` automatically when set.
#
# Notes:
#   - Only lines with the `# v<semver>` comment convention are validated.
#     Lines with a `# <tag>` comment containing spaces after the tag (e.g.
#     `# v6.5.2 (supports golangci-lint v1.x)`) are also matched; the
#     script extracts the first whitespace-delimited word after `#` as the
#     tag.
#   - Commented-out lines (leading `#`) in workflow YAML are ignored.
#   - Exits 0 when all pins are consistent; exits 1 on any mismatch or
#     API error.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Default: all yml files under .github/workflows/
if [[ $# -gt 0 ]]; then
    WORKFLOW_FILES=("$@")
else
    mapfile -t WORKFLOW_FILES < <(find "${REPO_ROOT}/.github/workflows" -name '*.yml' -type f | sort)
fi

if [[ ${#WORKFLOW_FILES[@]} -eq 0 ]]; then
    echo "check-action-pins: no workflow files found" >&2
    exit 0
fi

ERRORS=0

resolve_tag_to_commit_sha() {
    local owner_repo="$1"
    local tag="$2"

    # Step 1: resolve the ref to get its object SHA.
    local ref_sha
    ref_sha=$(gh api "repos/${owner_repo}/git/refs/tags/${tag}" --jq '.object.sha' 2>/dev/null) || {
        echo "  ERROR: could not resolve tag '${tag}' for ${owner_repo} via GitHub API" >&2
        return 1
    }

    # Step 2: if the object is an annotated tag (type=tag), dereference it
    # to the underlying commit SHA.
    local obj_type
    obj_type=$(gh api "repos/${owner_repo}/git/refs/tags/${tag}" --jq '.object.type' 2>/dev/null) || {
        echo "  ERROR: could not determine object type for tag '${tag}' in ${owner_repo}" >&2
        return 1
    }

    if [[ "${obj_type}" == "tag" ]]; then
        # Annotated tag — peel to commit.
        local commit_sha
        commit_sha=$(gh api "repos/${owner_repo}/git/tags/${ref_sha}" --jq '.object.sha' 2>/dev/null) || {
            echo "  ERROR: could not dereference annotated tag object ${ref_sha} in ${owner_repo}" >&2
            return 1
        }
        echo "${commit_sha}"
    else
        # Lightweight tag — ref already points to the commit.
        echo "${ref_sha}"
    fi
}

for wf_file in "${WORKFLOW_FILES[@]}"; do
    rel_path="${wf_file#${REPO_ROOT}/}"

    # Extract non-commented `uses:` lines that have a `# <tag>` comment.
    # Pattern: optional leading whitespace, NOT a leading `#`, then
    # `uses: owner/repo@<sha>  # <tag>...`
    while IFS= read -r line; do
        # Skip blank or pure-comment lines.
        [[ -z "${line}" ]] && continue
        stripped="${line#"${line%%[! ]*}"}"  # ltrim whitespace
        [[ "${stripped}" == \#* ]] && continue

        # Extract owner/repo, sha, and tag from the uses: line.
        # Regex: uses: <owner>/<repo>@<sha> # <tag>
        if [[ "${line}" =~ uses:[[:space:]]+([^@]+)@([0-9a-f]{40})[[:space:]]+#[[:space:]]+([^[:space:]]+) ]]; then
            owner_repo="${BASH_REMATCH[1]}"
            pinned_sha="${BASH_REMATCH[2]}"
            claimed_tag="${BASH_REMATCH[3]}"

            # Strip trailing punctuation that may appear after the tag word.
            claimed_tag="${claimed_tag%,}"
            claimed_tag="${claimed_tag%.}"

            printf "  checking %s@%s... (claimed: %s)\n" "${owner_repo}" "${pinned_sha:0:12}" "${claimed_tag}"

            resolved_sha=$(resolve_tag_to_commit_sha "${owner_repo}" "${claimed_tag}") || {
                ERRORS=$((ERRORS + 1))
                continue
            }

            if [[ "${pinned_sha}" == "${resolved_sha}" ]]; then
                printf "    OK: %s %s == %s\n" "${owner_repo}" "${claimed_tag}" "${pinned_sha:0:12}"
            else
                printf "    MISMATCH in %s:\n" "${rel_path}" >&2
                printf "      action:        %s\n" "${owner_repo}" >&2
                printf "      claimed tag:   %s\n" "${claimed_tag}" >&2
                printf "      pinned SHA:    %s\n" "${pinned_sha}" >&2
                printf "      expected SHA:  %s  (resolved from tag)\n" "${resolved_sha}" >&2
                ERRORS=$((ERRORS + 1))
            fi
        fi
    done < "${wf_file}"
done

echo ""
if [[ "${ERRORS}" -eq 0 ]]; then
    echo "check-action-pins: all pins consistent."
    exit 0
else
    echo "check-action-pins: ${ERRORS} mismatch(es) found — update the SHA or the tag comment." >&2
    exit 1
fi
