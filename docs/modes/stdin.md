# STDIN stream mode

When no file argument is given, `nesdit` reads from **stdin** and writes to **stdout**. This makes it a drop-in filter in shell pipelines.

Use `--format` to specify the input format (or pass `-` as the file argument):

```sh
cat config.yaml | nesdit --format yaml --query '.version = 2'
```

## Single-document STDIN

```sh
echo '{"x":1,"y":2}' | nesdit --format json --query '.x = 99'
```

**Stdout:**

```json
{"x":99,"y":2}
```

## Multi-document YAML stream

YAML documents separated by `---` are each transformed independently:

```sh
nesdit --format yaml --query '.version = 2' < stream.yaml
```

**Input (`stream.yaml`):**

```yaml
name: alpha
version: 1
---
name: beta
version: 1
---
name: gamma
version: 1
```

**Stdout:**

```yaml
---
name: alpha
version: 2
---
name: beta
version: 2
---
name: gamma
version: 2
```

Key order is preserved per document (NFR-3): `name` appears before `version` in each output document because it appeared before `version` in each input document.

## JSONL stream

JSON Lines (one JSON object per line) are also supported. Each line is treated as a separate document:

```sh
printf '{"id":1}\n{"id":2}\n{"id":3}\n' | nesdit --format json --create-missing --query '.processed = true'
```

**Stdout:**

```
{"id":1,"processed":true}
{"id":2,"processed":true}
{"id":3,"processed":true}
```

## TOML

TOML is single-document only. Piping a multi-document TOML stream is an error:

```sh
cat multi.toml | nesdit --format toml --query '.'
```

```
nesdit: error: -: format.unsupported: TOML input contains multiple top-level documents; TOML is single-doc only
```

## --where: selective transform

Transform only documents matching a predicate; others pass through unchanged:

```sh
nesdit --format yaml --where '.env == "prod"' --query '.version = 99' < input.yaml
```

**Input:**

```yaml
env: prod
version: 1
---
env: staging
version: 1
---
env: prod
version: 1
```

**Stdout:**

```yaml
---
env: prod
version: 99
---
env: staging
version: 1
---
env: prod
version: 99
```

Skipped documents emit a warning on stderr:

```
nesdit: warn: -:2: where.skipped: document did not match --where predicate
```

## --strict and --keep-going

With `--strict` (the default), processing halts at the first document error:

```sh
# Stream where doc 3 causes a query error — halts at doc 3.
nesdit --format yaml --query '.name | ascii_downcase' < stream.yaml
```

With `--keep-going`, errored documents are skipped and processing continues:

```sh
nesdit --format yaml --query '.name | ascii_downcase' --keep-going < stream.yaml
```

Stderr shows the per-document error:

```
nesdit: error: -:3: query.error: string required but got number
```

The run exits `1` if any document failed, even with `--keep-going`.

## Cross-format transcoding on STDIN

```sh
cat input.json | nesdit --format json --output-format yaml
```

**Input (`input.json`):**

```json
{"name":"alice","version":1}
```

**Stdout:**

```yaml
---
name: alice
version: 1
```

!!! note
    In STDIN stream mode, `nesdit` adds `---` separators between documents in YAML output. In file→stdout mode, single-document YAML output does not include a leading `---`.

## Related flags

- [`--format`](../reference/nesdit.md) — override format detection for STDIN.
- [`--where`](../reference/nesdit.md) — filter documents by a jq predicate.
- [`--strict`](../reference/nesdit.md) / [`--keep-going`](../reference/nesdit.md) — error policy for multi-document streams.
- [`--output-format`](../reference/nesdit.md) — transcode output to a different format.
