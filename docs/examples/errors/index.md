# Error diagnostics

Every `nesdit` error follows the canonical stderr shape:

```
nesdit: <severity>: [<file>[:<index>]: ]<event>: <message>
```

- `<severity>` — `error`, `warn`, or `info`.
- `<file>` — the source file path, or `-` for STDIN. Omitted for flag-parse errors.
- `<index>` — 1-based document index in a multi-doc stream. Omitted for single-doc inputs.
- `<event>` — a token from the shared event enum (e.g., `format.mixed`, `query.error`).
- `<message>` — human-readable detail. No trailing period.

---

## Mixed-format invocation (FR-5)

Passing files of different formats in one invocation is rejected before any IO:

```sh
nesdit -i a.json b.yaml --query '.x = 1'
```

```
nesdit: error: format.mixed: yaml (b.yaml), json (a.json): single invocation spans multiple formats
```

Exit code: `1`. Neither file is modified. No stdout.

---

## Cross-format incompatibility (FR-19)

### null → TOML

TOML cannot represent `null`. Attempting to set a value to `null` in a TOML file fails with a path-aware error:

```sh
nesdit config.toml --query '.k = null'
```

```
nesdit: error: config.toml: format.incompatible: TOML cannot represent null at .k
```

Exit code: `1`.

### NaN / Inf → YAML or TOML

```sh
# A query that produces NaN
nesdit data.yaml --query '.ratio = (1 / 0)'
```

```
nesdit: error: data.yaml: format.incompatible: YAML cannot represent Inf at .ratio
```

---

## --create-missing: missing path failure

Without `--create-missing`, assigning to a path that does not exist is an error:

**Input (`config.json`):**

```json
{"a":1}
```

```sh
nesdit config.json --query '.b.c = 2'
```

```
nesdit: error: config.json: query.missing_path: path .b does not exist; use --create-missing to allow path creation
```

Exit code: `1`. The file is unchanged.

**Fix:** add `--create-missing`:

```sh
nesdit config.json --create-missing --query '.b.c = 2'
```

**Stdout:**

```json
{"a":1,"b":{"c":2}}
```

---

## Parse error — malformed input

**Input (`bad.yaml`):**

```yaml
key: value
key: duplicate
```

```sh
nesdit bad.yaml --query '.'
```

```
nesdit: error: bad.yaml: parse.error: mapping key duplicated: "key"
```

Exit code: `1`. Use `--yaml-version 1.1` to allow duplicate keys (last wins, with a warning):

```sh
nesdit --yaml-version 1.1 bad.yaml --query '.'
```

```
nesdit: warn: bad.yaml: parse.warn: duplicate key "key"; using last value
```

!!! note "Available in v1"
    The `--yaml-version` flag was included in v1 scope despite appearing in the RFC's deferral list. It is fully supported.

---

## Flag conflict errors (DR-001 matrix)

Every ERROR cell in the flag interaction matrix produces a `flag.conflict` or `flag.invalid` error before any file is read.

### --dry-run + --check

```sh
nesdit config.json -n --check --query '.'
```

```
nesdit: error: flag.conflict: --dry-run and --check are mutually exclusive: --dry-run prints a diff, --check only signals drift via exit code
```

### --backup without -i

```sh
nesdit config.json --backup --query '.'
```

```
nesdit: error: flag.invalid: --backup requires -i
```

### --edit + -i

```sh
nesdit --edit -i config.yaml
```

```
nesdit: error: flag.conflict: --edit and -i are mutually exclusive: --edit always prints to stdout; use the emitted query with -i in a second invocation
```

### --edit + --dry-run

```sh
nesdit --edit --dry-run config.yaml
```

```
nesdit: error: flag.conflict: --edit is interactive and emits its own preview; --dry-run is not compatible
```

### --edit + --check

```sh
nesdit --edit --check config.yaml
```

```
nesdit: error: flag.conflict: --edit is interactive; --check is for non-interactive drift detection
```

### --output-format + -i

```sh
nesdit -i values.json --output-format yaml --query '.'
```

```
nesdit: error: flag.conflict: --output-format and -i are mutually exclusive: --output-format with -i would write a different format into the same file; use a separate output path
```

### --from-file + --query together

```sh
nesdit config.json --query '.' -f q.jq
```

```
nesdit: error: flag.conflict: --query and --from-file are mutually exclusive
```

### --keep-going + --strict together

```sh
nesdit config.json --query '.' --keep-going --strict
```

```
nesdit: error: flag.conflict: --keep-going and --strict are mutually exclusive
```

---

## --argjson decode failure

```sh
nesdit a.json --argjson v='not-json' --query '.v = $v'
```

```
nesdit: error: arg.decode: --argjson v: expected JSON, got "not-json"
```

Exit code: `1`.

---

## TOML multi-doc input

TOML is single-document only. Piping what looks like multiple TOML documents fails:

```sh
cat multi.toml | nesdit --format toml --query '.'
```

```
nesdit: error: -: format.unsupported: TOML input contains multiple top-level documents; TOML is single-doc only
```

---

## --check exit code reference

| Scenario | Exit code |
|---|---|
| Identity query, no drift | `0` |
| Query would change the input | `2` |
| File fails to parse | `1` |
| Cross-format `--check` (always drifts) | `2` |
| Flag error (e.g., bad `--output-format` value) | `1` |

Exit `2` is reserved exclusively for `--check` drift detection. No other flag or error path produces exit `2`.
