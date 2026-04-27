# Pipeline and batch patterns

## Glob expansion

Shell globbing passes multiple files to `nesdit` in a single invocation. Quote the glob to prevent premature expansion if you want `nesdit` to process whatever the shell expands at run time:

```sh
nesdit -i *.yaml --query '.image.tag = "v2.0"'
```

After the run, `nesdit` prints a summary to stderr:

```
nesdit: info: batch.summary: 5 changed, 0 unchanged, 0 errored
```

!!! note
    `nesdit` does not perform its own glob expansion. The shell expands the glob before passing file names to `nesdit`. If the glob matches zero files, the shell (depending on `nullglob` / `noglob` settings) may pass the literal `*.yaml` as a filename — `nesdit` will then report "no such file" rather than silently succeeding.

---

## --keep-going: partial-failure tolerance

By default (`--strict`), `nesdit` halts on the first document or file error. Use `--keep-going` to process all remaining files and collect all errors:

```sh
nesdit -i ok1.json bad.json ok3.json --query '.value |= ascii_upcase' --keep-going
```

- `ok1.json` and `ok3.json` — updated.
- `bad.json` — skipped (query failed on this file); its bytes are unchanged.
- Exit code: `1` (errors occurred).

Stderr:

```
nesdit: error: bad.json: query.error: string required but got number
nesdit: info: batch.summary: 2 changed, 0 unchanged, 1 errored
```

Under `--strict` (default), NFR-7 applies: if any file fails during the encode pass, **no files** are written:

```sh
nesdit -i ok1.json bad.json ok3.json --query '.value |= ascii_upcase'
```

All three files are unchanged. Exit code: `1`.

---

## --strict: zero-tolerance pipelines

`--strict` is the default. In a CI pipeline where any failure should halt the job, you don't need to pass it explicitly:

```sh
nesdit -i **/*.yaml --query '.image.tag = "v2.0"'
```

If any file fails to parse, encode, or is the wrong format, `nesdit` exits `1` and no files are written.

---

## Pre-commit hook with --check

Create `.pre-commit-hooks.yaml`:

```yaml
- id: nesdit-check
  name: Detect config drift
  language: system
  entry: nesdit --check --query '.'
  files: ^config/.*\.(yaml|json|toml)$
  pass_filenames: true
```

And register it in `.pre-commit-config.yaml`:

```yaml
repos:
  - repo: local
    hooks:
      - id: nesdit-check
```

The hook runs `nesdit --check --query '.' <file>` for each staged config file. Exit `2` (drift detected) blocks the commit.

---

## CI step — drift gate with JSON logging

Use `--log-format=json` to get machine-readable output, then pipe to `jq` to extract events or fail with a readable message:

```yaml
# .github/workflows/ci.yml
- name: Check config tag is current
  run: |
    nesdit config/values.yaml --check \
      --query '.image.tag = "${{ env.RELEASE_TAG }}"' \
      --log-format=json 2>&1 | tee /tmp/nesdit.log
    exit ${PIPESTATUS[0]}
```

Parse failures or drift appear as NDJSON on stderr:

```json
{"ts":"2026-04-28T10:22:04.512Z","level":"error","event":"query.error","path":"config/values.yaml","message":"path .image.tag does not exist","schema_v":1}
```

---

## Dry-run preview before in-place edit

A common pattern: preview the diff first, then apply only if it looks correct.

```sh
# Step 1: preview
nesdit config.yaml -n --query '.image.tag = "v2.0"'
```

```diff
--- config.yaml
+++ config.yaml
@@ -2 +2 @@
-  tag: v1.9
+  tag: v2.0
```

```sh
# Step 2: apply
nesdit -i config.yaml --query '.image.tag = "v2.0"'
```

---

## Full CI example — check then apply

```yaml
# .github/workflows/release.yml
- name: Bump image tag (dry-run preview)
  run: |
    nesdit helm/values.yaml -n --query '.image.tag = "${{ env.VERSION }}"'

- name: Check current tag
  run: |
    nesdit helm/values.yaml --check --query '.image.tag = "${{ env.VERSION }}"'
  # Exits 0 if already set. Exits 2 if tag would change (not an error in this step).
  continue-on-error: true

- name: Apply tag update
  run: |
    nesdit -i helm/values.yaml --query '.image.tag = "${{ env.VERSION }}"'
    git add helm/values.yaml
    git commit -m "chore: bump image tag to ${{ env.VERSION }}"
```

---

## Bulk update across repositories (shell loop)

```sh
for dir in repo1 repo2 repo3; do
  nesdit -i "$dir/config/values.yaml" \
    --query '.image.tag = "v2.0"' \
    --log-format=json 2>&1
done
```

Each run appends a `batch.summary` NDJSON event. Pipe to `jq` to aggregate:

```sh
for dir in repo1 repo2 repo3; do
  nesdit -i "$dir/config/values.yaml" --query '.image.tag = "v2.0"' --log-format=json 2>&1
done | jq -r 'select(.event == "batch.summary") | "\(.path // "batch") \(.message)"'
```
