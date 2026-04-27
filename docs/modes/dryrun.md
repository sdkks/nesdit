# Dry-run mode (`-n` / `--dry-run`)

`--dry-run` (short: `-n`) prints a unified diff of the before and after document to stdout. It **never writes any file**.

Dry-run always exits `0` on success — a non-empty diff is informational, not a gating signal. Use [`--check`](check.md) if you need to gate on drift.

## Basic usage

```sh
nesdit config.json -n --query '.x = 99'
```

**Input (`config.json`):**

```json
{"x":1,"y":2}
```

**Stdout (unified diff):**

```diff
--- config.json
+++ config.json
@@ -1 +1 @@
-{"x":1,"y":2}
+{"x":99,"y":2}
```

The file on disk is unchanged.

## YAML dry-run

```sh
nesdit deploy.yaml -n --query '.value = "world"'
```

**Input (`deploy.yaml`):**

```yaml
name: test
value: hello
```

**Stdout:**

```diff
--- deploy.yaml
+++ deploy.yaml
@@ -1,2 +1,2 @@
 name: test
-value: hello
+value: world
```

## No-op query

If the query produces no change, the diff body is empty. The command still exits `0`:

```sh
nesdit config.json -n --query '.'
```

## Combining with -i

`-i` and `-n` can both be passed. `--dry-run` wins: the file is not written and a flag-precedence warning is emitted on stderr:

```sh
nesdit -i config.json -n --query '.x = 99'
```

Stderr:

```
nesdit: warn: flag.precedence: -i ignored because --dry-run is set
```

This is intentional: CI scripts often use `-i` as a baseline flag and toggle `-n` to preview what would happen without actually running the edit.

## Use in CI — preview before commit

```yaml
# .github/workflows/ci.yml
- name: Preview config changes (dry-run)
  run: nesdit config/values.yaml -n --query '.image.tag = "${{ github.sha }}"'
```

The step succeeds whether or not there is a diff. Pipe the output to a comment or log for review.

!!! warning
    `--dry-run` and `--check` are mutually exclusive:
    ```
    nesdit config.json -n --check --query '.'
    ```
    ```
    nesdit: error: flag.conflict: --dry-run and --check are mutually exclusive: --dry-run prints a diff, --check only signals drift via exit code
    ```

## Related flags

- [`--check`](check.md) — signal drift via exit code without printing a diff.
- [`-i` / `--in-place`](in-place.md) — write back to disk.
- [`--query`](../reference/nesdit.md) — the jq-style query to apply.
