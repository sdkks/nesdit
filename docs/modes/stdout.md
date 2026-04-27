# Stdout mode (default)

Without `-i`, `nesdit` reads the input file, applies the query, and writes the result to **stdout**. The file on disk is never touched.

## Basic usage

```sh
nesdit config.json --query '.env = "production"'
```

**Input (`config.json`):**

```json
{"env":"staging","replicas":1}
```

**Stdout:**

```json
{"env":"production","replicas":1}
```

**`config.json` on disk:** unchanged.

## Piping to another command

Because the result goes to stdout, you can pipe it anywhere:

```sh
nesdit values.yaml --query '.image.tag = "v2.0"' | kubectl apply -f -
```

Or redirect to a new file:

```sh
nesdit staging.json --query '.env = "production"' > production.json
```

## Cross-format transcoding

Use `--output-format` to produce a different format than the input:

```sh
nesdit input.json --output-format yaml
```

**Input (`input.json`):**

```json
{"name":"alice","version":1}
```

**Stdout:**

```yaml
name: alice
version: 1
```

See [examples/formats](../examples/formats/index.md) for all six format combinations.

## Identity query

The default query is `.` (identity). If you omit `--query`, `nesdit` normalises and round-trips the file through its decoder and encoder:

```sh
nesdit config.json
```

This is useful for normalising a file to `nesdit`'s canonical encoding. The output is always idempotent: running it a second time produces byte-identical output.

## Multiple files to stdout

When multiple files are passed (all the same format), outputs are concatenated with the appropriate framing:

- YAML files: `---` separators between documents.
- JSON files: one JSON object per line (JSONL).

```sh
nesdit a.yaml b.yaml --query '.version = 2'
```

**Output:**

```yaml
---
name: alpha
version: 2
---
name: beta
version: 2
```

!!! warning
    Mixing formats in one invocation is an error:
    ```
    nesdit a.json b.yaml --query '.'
    ```
    ```
    nesdit: error: format.mixed: yaml (b.yaml), json (a.json): single invocation spans multiple formats
    ```

## Related flags

- [`-i` / `--in-place`](in-place.md) — write back to disk instead of stdout.
- [`--output-format`](../reference/nesdit.md) — transcode to a different format.
- [`--format`](../reference/nesdit.md) — override input format detection.
- [`--query`](../reference/nesdit.md) — the jq-style query to apply.
