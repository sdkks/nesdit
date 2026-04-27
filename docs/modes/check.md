# Check mode (`--check`)

`--check` applies the query in a read-only way and signals the result via exit code. It never writes any file or prints to stdout.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | The query produces output byte-identical to the input — no drift. |
| `1` | An error occurred (parse failure, bad query, IO error, bad flags, etc.). |
| `2` | The query would change the input — drift detected. |

!!! tip
    Exit code `2` is reserved **exclusively** for `--check` drift detection. No other flag or error produces exit `2`. This makes it safe to use in `case $?` scripts:
    ```sh
    case $? in
      0) echo "up to date" ;;
      2) echo "drift detected" ;;
      *) echo "error" ;;
    esac
    ```

## Basic usage

### No drift (exit 0)

```sh
nesdit config.json --check --query '.'
echo "exit: $?"
```

The identity query `.` produces no change. Exits `0`. STDOUT and STDERR are empty.

### Drift detected (exit 2)

```sh
nesdit config.json --check --query '.x = 99'
echo "exit: $?"
```

The query would change `.x`. Exits `2`. STDOUT is empty. The file on disk is unchanged.

### Parse error (exit 1)

```sh
nesdit broken.yaml --check --query '.'
echo "exit: $?"
```

If the file fails to parse, exits `1` — errors dominate drift signaling.

## Pre-commit hook example

Create `.pre-commit-hooks.yaml` in your repo:

```yaml
- id: nesdit-check-values
  name: Check Helm values drift
  language: system
  entry: nesdit --check --query '.'
  files: ^helm/values.*\.yaml$
  pass_filenames: true
```

The hook fails (non-zero exit) if any matched file would be changed by the query — catching uncommitted hand-edits that conflict with the committed query.

## CI step example

```yaml
# .github/workflows/ci.yml
- name: Check config is up to date
  run: |
    nesdit config/values.yaml --check --query '.image.tag = "${{ env.TARGET_TAG }}"'
    # Exits 0 if already set, 2 if drift, 1 if error.
```

## --check with --output-format

When `--output-format` differs from the input format, `--check` always exits `2` — even with an identity query:

```sh
nesdit input.json --check --output-format yaml --query '.'
echo "exit: $?"
```

Exits `2`. The two formats serialize differently, so byte-level comparison always shows drift.

For same-format checks:

```sh
nesdit input.json --check --output-format json --query '.'
echo "exit: $?"
```

Exits `0` — same format, identity query, no drift.

## --check with -i

`-i` and `--check` can both be passed. `--check` wins: no file is written and a flag-precedence warning is emitted on stderr:

```sh
nesdit -i config.json --check --query '.x = 99'
```

Stderr:

```
nesdit: warn: flag.precedence: -i ignored because --check is set
```

## Related flags

- [`--dry-run`](dryrun.md) / `-n` — print a diff instead of signaling via exit code.
- [`--log-format=json`](../reference/nesdit.md) — emit NDJSON events on stderr for machine consumption.
- [`--output-format`](../reference/nesdit.md) — transcode to a different format (always drifts cross-format).
