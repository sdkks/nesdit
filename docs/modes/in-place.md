# In-place mode (`-i`)

In-place mode edits one or more files directly on disk. Every write is **atomic**: `nesdit` writes to a temporary file in the same directory, then renames it over the target. The target file is either fully updated or fully unchanged — no partial state is ever observable.

## Basic usage

```sh
nesdit -i config.json --query '.env = "production"'
```

**Input (`config.json`):**

```json
{"env":"staging","replicas":1}
```

**After the command (`config.json` on disk):**

```json
{"env":"production","replicas":1}
```

STDOUT is empty. STDERR is empty on success.

## YAML in-place

```sh
nesdit -i deploy.yaml --query '.replicas = 3'
```

**Input (`deploy.yaml`):**

```yaml
name: myapp
replicas: 1
```

**After the command:**

```yaml
name: myapp
replicas: 3
```

## TOML in-place

```sh
nesdit -i Chart.toml --query '.version = "2.0"'
```

**Input (`Chart.toml`):**

```toml
version = "1.0"
name = "project"
```

**After the command:**

```toml
version = "2.0"
name = "project"
```

!!! note
    `nesdit` emits TOML using inline-table syntax for nested structures to preserve your key order exactly. Idiomatic `[section]` TOML is read correctly on input.

## Batch mode (multiple files)

Pass multiple files or a shell glob:

```sh
nesdit -i f1.json f2.json f3.json --create-missing --query '.mark = true'
```

After a multi-file run, `nesdit` prints a summary to stderr:

```
nesdit: info: batch.summary: 3 changed, 0 unchanged, 0 errored
```

!!! tip
    Shell globbing (`nesdit -i '**/*.yaml' --query '...'`) works because the shell expands the glob before passing file names to `nesdit`. `nesdit` does not perform its own glob expansion.

## --backup: keep a copy of the original

Add `--backup` to save a sibling file before each atomic write:

```sh
nesdit -i config.json --backup --query '.version = "2.0"'
```

This creates `config.json.bak` containing the original bytes. Use a custom suffix with `--backup=.orig`:

```sh
nesdit -i config.json --backup=.orig --query '.version = "2.0"'
```

`nesdit` emits a confirmation to stderr:

```
nesdit: info: config.json: file.backup_written: config.json.bak written
```

!!! warning
    `--backup` requires `-i`. Using `--backup` without `-i` is a flag-parse error:
    ```
    nesdit: error: flag.invalid: --backup requires -i
    ```

## Symlinks

When the target is a symlink, `nesdit` follows it and edits the real file. The symlink itself is preserved.

## Flag interactions

| Flag combination | Behaviour |
|---|---|
| `-i` + `-n`/`--dry-run` | `--dry-run` wins. No file written. A warning is emitted: `"-i ignored because --dry-run is set"`. |
| `-i` + `--check` | `--check` wins. No file written. A warning is emitted. Exits `0` if no drift, `2` if drift. |
| `-i` + `--backup` | Allowed. Backup is written before the atomic rename. |
| `-i` + `--edit` | Error: `"--edit and -i are mutually exclusive"`. |
| `-i` + `--output-format` | Error: `"--output-format and -i are mutually exclusive"`. |

See the [DR-001 flag matrix](../reference/nesdit.md) and [modes/check](check.md) for full details.

## Related flags

- [`--backup`](../reference/nesdit.md) — sibling backup before write.
- [`--dry-run`](dryrun.md) / `-n` — preview the diff without writing.
- [`--check`](check.md) — gate on drift without writing.
- [`--keep-going`](../reference/nesdit.md) — continue after per-file errors in a batch.
- [`--create-missing`](../reference/nesdit.md) — allow queries to create new keys.
