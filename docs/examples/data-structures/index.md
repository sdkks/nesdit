# Data structure examples

## Scalars

### String

**Input (`a.json`):**

```json
{"name":"alice","version":"1.0"}
```

**Read a string field:**

```sh
nesdit a.json --query '.name'
```

**Stdout:** `"alice"`

**Set a string field:**

```sh
nesdit a.json --query '.name = "bob"'
```

**Stdout:**

```json
{"name":"bob","version":"1.0"}
```

### Integer

**Input:**

```json
{"count":0}
```

**Set:**

```sh
nesdit a.json --query '.count = 42'
```

**Stdout:** `{"count":42}`

**Increment with `|=`:**

```sh
nesdit a.json --query '.count |= . + 1'
```

**Stdout:** `{"count":1}`

### Float

**Input:**

```json
{"ratio":0.5}
```

```sh
nesdit a.json --query '.ratio = 1.25'
```

**Stdout:** `{"ratio":1.25}`

### Boolean

**Input:**

```json
{"enabled":false}
```

```sh
nesdit a.json --query '.enabled = true'
```

**Stdout:** `{"enabled":true}`

### Null

**Input:**

```json
{"key":"value"}
```

**Set a field to null:**

```sh
nesdit a.json --query '.key = null'
```

**Stdout:** `{"key":null}`

!!! warning
    TOML cannot represent `null`. Setting a field to `null` in a TOML file is an error:
    ```
    nesdit: error: config.toml: format.incompatible: TOML cannot represent null at .key
    ```

---

## Nested objects

**Input (`config.json`):**

```json
{"app":{"name":"myapp","settings":{"timeout":30,"retries":3}}}
```

**Deep key access:**

```sh
nesdit config.json --query '.app.settings.timeout'
```

**Stdout:** `30`

**Nested mutation:**

```sh
nesdit config.json --query '.app.settings.timeout = 60'
```

**Stdout:**

```json
{"app":{"name":"myapp","settings":{"timeout":60,"retries":3}}}
```

**Partial update (preserves sibling keys):**

```sh
nesdit config.json --query '.app.name = "newapp"'
```

**Stdout:**

```json
{"app":{"name":"newapp","settings":{"timeout":30,"retries":3}}}
```

---

## Arrays

**Input (`data.json`):**

```json
{"items":[10,20,30,40,50]}
```

**Index access:**

```sh
nesdit data.json --query '.items[0]'
```

**Stdout:** `10`

**Update by index:**

```sh
nesdit data.json --query '.items[2] = 99'
```

**Stdout:** `{"items":[10,20,99,40,50]}`

**Map over all elements (`.[]=|= ...`):**

```sh
nesdit data.json --query '.items[] |= . * 2'
```

**Stdout:** `{"items":[20,40,60,80,100]}`

**Append an element:**

```sh
nesdit data.json --create-missing --query '.items += [99]'
```

**Stdout:** `{"items":[10,20,30,40,50,99]}`

**Delete by index:**

```sh
nesdit data.json --query 'del(.items[1])'
```

**Stdout:** `{"items":[10,30,40,50]}`

### Filter with `select`

`select(predicate)` keeps only elements for which the predicate is true. Wrap with array reconstruction to filter an array-of-maps:

**Input (`services.yaml`):**

```yaml
services:
  - name: api
    enabled: true
  - name: worker
    enabled: false
  - name: scheduler
    enabled: true
```

```sh
nesdit --format yaml --query '.services = [.services[] | select(.enabled == true)]' services.yaml
```

**Stdout:**

```yaml
services:
  - name: api
    enabled: true
  - name: scheduler
    enabled: true
```

Key order within each map entry is preserved. The `worker` entry is dropped because `.enabled == false`.

---

## Array-of-maps

**Input (`pods.yaml`):**

```yaml
pods:
  - name: web
    replicas: 1
    image: nginx
  - name: worker
    replicas: 2
    image: python
  - name: cache
    replicas: 1
    image: redis
```

**Update the first element:**

```sh
nesdit pods.yaml --query '.pods[0].replicas = 3'
```

**Stdout:**

```yaml
pods:
  - name: web
    replicas: 3
    image: nginx
  - name: worker
    replicas: 2
    image: python
  - name: cache
    replicas: 1
    image: redis
```

Key order within each map entry is preserved.

!!! note
    **Positional reconciliation:** When a jq expression reshapes an array (e.g., `sort_by`, `reverse`), each output element inherits key order from the element at the same positional index in the input. This is deterministic but may produce surprising key orderings if the array is reordered. This is a documented v1 behaviour (DR-007).

---

## Mixed-depth structures

**Input (`mixed.json`):**

```json
{"meta":{"tags":["a","b","c"],"counts":{"x":1,"y":2}}}
```

**Access array inside nested object:**

```sh
nesdit mixed.json --query '.meta.tags[1]'
```

**Stdout:** `"b"`

**Mutate deep inside:**

```sh
nesdit mixed.json --query '.meta.counts.x = 99'
```

**Stdout:**

```json
{"meta":{"tags":["a","b","c"],"counts":{"x":99,"y":2}}}
```

---

## YAML 1.1 vs 1.2 booleans

YAML 1.2 (the default) only recognises `true` and `false` as booleans. Values like `yes`, `no`, `on`, and `off` are plain strings.

**Input (`flags.yaml`):**

```yaml
enabled: yes
disabled: no
switch_on: on
switch_off: off
```

**Default (YAML 1.2 — `yes` is a string):**

```sh
nesdit flags.yaml --query '.enabled'
```

**Stdout:** `yes`

**With `--yaml-version 1.1` — `yes` is decoded as boolean `true`:**

```sh
nesdit --yaml-version 1.1 flags.yaml --query '.enabled'
```

**Stdout:** `true`

```sh
nesdit --yaml-version 1.1 flags.yaml --query '.disabled'
```

**Stdout:** `false`

```sh
nesdit --yaml-version 1.1 flags.yaml --query '.switch_on'
```

**Stdout:** `true`

!!! note
    The encoder always writes YAML 1.2 output regardless of `--yaml-version`. Setting `--yaml-version 1.1` only affects how the input is decoded.

!!! note "Available in v1"
    The `--yaml-version` flag was included in v1 scope despite appearing in the RFC's deferral list. It is fully supported.

---

## TOML-specific types

### Integer vs float

TOML distinguishes integers from floats at the type level. `nesdit` preserves this distinction:

**Input (`nums.toml`):**

```toml
count = 42
ratio = 3.14
```

**Update the integer:**

```sh
nesdit nums.toml --query '.count = 100'
```

**Stdout:** `count = 100\nratio = 3.14`

**Update to a float:**

```sh
nesdit nums.toml --query '.ratio = 2.718'
```

**Stdout:** `count = 42\nratio = 2.718`

### Inline tables

`nesdit` emits TOML nested objects as inline tables to preserve key order. Input with `[section]` syntax is read correctly:

**Input (`project.toml`):**

```toml
[package]
name = "mylib"
version = "0.1.0"
```

```sh
nesdit project.toml --query '.package.version = "0.2.0"'
```

**Stdout:**

```toml
package = {name = "mylib", version = "0.2.0"}
```

The `[section]` input was read; the output uses inline-table syntax. Round-trips are byte-stable: running the same command again on this output produces byte-identical output.
