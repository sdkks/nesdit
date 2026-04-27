# Format examples

`nesdit` reads and writes JSON, YAML, and TOML. By default the output format equals the input format. Use `--output-format` to transcode to a different format.

!!! note
    Comments are not preserved across round-trips. This is an explicit non-goal for v1. If your files contain comments that matter, keep the source-of-truth file elsewhere and generate the deployed copy from it.

## JSON → JSON

**Input (`config.json`):**

```json
{"x":1,"y":2}
```

**Identity round-trip (no mutation):**

```sh
nesdit config.json --query '.'
```

**Stdout:**

```json
{"x":1,"y":2}
```

Key order is preserved exactly. The output is byte-identical to the input.

**Field mutation:**

```sh
nesdit config.json --query '.x = 99'
```

**Stdout:**

```json
{"x":99,"y":2}
```

**Field deletion:**

```sh
nesdit config.json --create-missing --query 'del(.y)'
```

**Stdout:**

```json
{"x":1}
```

---

## YAML → YAML

**Input (`values.yaml`):**

```yaml
name: alpha
version: 1
```

**Identity round-trip:**

```sh
nesdit values.yaml --query '.'
```

**Stdout:**

```yaml
name: alpha
version: 1
```

**Multi-document stream:**

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

```sh
nesdit --format yaml --query '.version = 2' < stream.yaml
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

---

## TOML → TOML

**Input (`project.toml`):**

```toml
version = "1.0"
name = "project"
```

**Mutation:**

```sh
nesdit project.toml --query '.version = "2.0"'
```

**Stdout:**

```toml
version = "2.0"
name = "project"
```

!!! note
    `nesdit` always emits TOML using inline-table syntax for nested structures. This preserves your key order exactly. Your idiomatic `[section]` TOML input is read correctly; the output looks like `{key = value}` for nested tables. This is a deliberate trade-off for round-trip fidelity (see DR-006 in the spec).

---

## JSON → YAML (`--output-format=yaml`)

Cross-format transcoding: read JSON, write YAML.

**Input (`input.json`):**

```json
{"name":"alice","version":1}
```

**File mode (no `---` prefix for single-document output):**

```sh
nesdit input.json --output-format yaml
```

**Stdout:**

```yaml
name: alice
version: 1
```

**STDIN stream mode (adds `---` prefix):**

```sh
nesdit --format json --output-format yaml < input.json
```

**Stdout:**

```yaml
---
name: alice
version: 1
```

!!! warning
    Running `--check` on a cross-format invocation with an identity query always exits `2` — even when nothing changed semantically — because the two serializations are never byte-identical:
    ```sh
    nesdit input.json --check --output-format yaml --query '.'
    # exits 2
    ```

---

## YAML → JSON (`--output-format=json`)

**Input (`input.yaml`):**

```yaml
name: alice
version: 1
```

**File mode:**

```sh
nesdit input.yaml --output-format json
```

**Stdout:**

```json
{"name":"alice","version":1}
```

Key order is preserved: `name` precedes `version` in the input and in the output.

---

## YAML → TOML (`--output-format=toml`)

**Input (`input.yaml`):**

```yaml
name: alice
version: 1
```

```sh
nesdit input.yaml --output-format toml
```

**Stdout:**

```toml
name = "alice"
version = 1
```

---

## JSON → TOML (`--output-format=toml`)

**Input (`input.json`):**

```json
{"name":"alice","version":1}
```

```sh
nesdit input.json --output-format toml
```

**Stdout:**

```toml
name = "alice"
version = 1
```

---

## TOML → YAML (`--output-format=yaml`)

**Input (`input.toml`):**

```toml
name = "alice"
version = 1
```

**File mode:**

```sh
nesdit input.toml --output-format yaml
```

**Stdout:**

```yaml
name: alice
version: 1
```

---

## --output-format conflict with -i

`--output-format` and `-i` are mutually exclusive. Writing a different format back into the same file would silently corrupt it:

```sh
nesdit -i values.json --output-format yaml --query '.'
```

```
nesdit: error: flag.conflict: --output-format and -i are mutually exclusive: --output-format with -i would write a different format into the same file; use a separate output path
```

Use redirection instead:

```sh
nesdit values.json --output-format yaml --query '.' > values.yaml
```
