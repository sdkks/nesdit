# Expression examples

## Identity query (`.`)

The identity query passes the document through with no mutation. It is the default when neither `--query` nor `-f` is given.

**Input (`config.json`):**

```json
{"x":1,"y":2}
```

```sh
nesdit config.json --query '.'
```

**Stdout:**

```json
{"x":1,"y":2}
```

Useful for normalising a file to `nesdit`'s canonical encoding without changing any values.

---

## Field access and mutation

### Set a field

```sh
nesdit config.json --query '.x = 99'
```

**Stdout:** `{"x":99,"y":2}`

### Update in-place with `|=`

`|=` applies an expression to the current value of the field:

```sh
nesdit config.json --query '.x |= . + 1'
```

**Stdout:** `{"x":2,"y":2}` (incremented by 1)

### Chained mutations

Multiple assignments can be chained with `|`:

```sh
nesdit config.json --query '.x = 10 | .y = 20'
```

**Stdout:** `{"x":10,"y":20}`

---

## Deletion

### Delete a key

```sh
nesdit config.json --query 'del(.y)'
```

**Stdout:** `{"x":1}`

### Delete an array element by index

**Input:**

```json
{"items":[10,20,30,40,50]}
```

```sh
nesdit data.json --query 'del(.items[2])'
```

**Stdout:** `{"items":[10,20,40,50]}`

---

## Conditionals

Apply a query only when a condition is met:

**Input (`env.yaml`):**

```yaml
env: prod
replicas: 1
```

```sh
nesdit env.yaml --query 'if .env == "prod" then .replicas = 3 else . end'
```

**Stdout:**

```yaml
env: prod
replicas: 3
```

If `.env` were `"staging"`, the document would pass through unchanged.

---

## String interpolation

Build a string from a field value using jq's `\(.field)` syntax:

**Input:**

```json
{"name":"myapp","version":"1.0"}
```

```sh
nesdit app.json --query '.name = "prefix-\(.name)"'
```

**Stdout:**

```json
{"name":"prefix-myapp","version":"1.0"}
```

---

## `--arg`: inject a shell variable as a string

`--arg K=V` binds `$K` in the query as a **literal string**. Numbers passed this way remain strings.

**Input (`a.json`):**

```json
{}
```

```sh
nesdit a.json --create-missing --arg name=alice --query '.user = $name'
```

**Stdout:**

```json
{"user":"alice"}
```

Numbers stay as strings with `--arg`:

```sh
nesdit a.json --create-missing --arg count=42 --query '.count = $count'
```

**Stdout:**

```json
{"count":"42"}
```

---

## `--argjson`: inject a shell variable as a typed JSON value

`--argjson K=V` parses `V` as JSON and binds it as a typed value. Use this for numbers, booleans, arrays, and objects.

**Integer:**

```sh
nesdit a.json --create-missing --argjson count=42 --query '.count = $count'
```

**Stdout:**

```json
{"count":42}
```

**Array:**

```sh
nesdit a.json --create-missing --argjson nums='[1,2,3]' --query '.items = $nums'
```

**Stdout:**

```json
{"items":[1,2,3]}
```

**Object:**

```sh
nesdit a.json --create-missing --argjson cfg='{"a":1}' --query '.cfg = $cfg'
```

**Stdout:**

```json
{"cfg":{"a":1}}
```

If the value is not valid JSON, `nesdit` exits `1` with an error:

```sh
nesdit a.json --argjson v='not-json' --query '.v = $v'
```

```
nesdit: error: arg.decode: --argjson v: expected JSON, got "not-json"
```

---

## `--from-file`: load a query from a `.jq` file

For complex or reusable queries, store them in a file:

**`q.jq`:**

```jq
.z = 99
```

**Input (`a.json`):**

```json
{"x":1,"y":2}
```

```sh
nesdit a.json --from-file q.jq --create-missing
```

**Stdout:**

```json
{"x":1,"y":2,"z":99}
```

The short form is `-f`:

```sh
nesdit a.json -f q.jq --create-missing
```

!!! warning
    `--query` and `--from-file` are mutually exclusive:
    ```sh
    nesdit a.json --query '.' -f q.jq
    ```
    ```
    nesdit: error: flag.conflict: --query and --from-file are mutually exclusive
    ```

---

## `--where`: apply query only to matching documents

In a multi-document stream, `--where` restricts the query to documents that satisfy a jq predicate. Other documents pass through unchanged.

**Input (`stream.yaml`):**

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

```sh
nesdit --format yaml --where '.env == "prod"' --query '.version = 99' < stream.yaml
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

Stderr (for the skipped document):

```
nesdit: warn: -:2: where.skipped: document did not match --where predicate
```

---

## `$${VAR}` escape — literal `${VAR}` in a query

`nesdit` never interpolates shell environment variables. If you need the literal string `${FOO}` in a value, escape the `$` by doubling it:

```sh
nesdit a.json --create-missing --query '.x = "$${FOO}"'
```

**Stdout:**

```json
{"x":"${FOO}"}
```

The shell environment variable `FOO` is never read. `$${FOO}` is always the literal string `${FOO}`.

---

## `--create-missing`: allow queries to create new keys

By default, assigning to a path that does not exist is an error:

**Input:**

```json
{"a":1}
```

```sh
nesdit config.json --query '.b.c = 2'
```

```
nesdit: error: config.json: query.missing_path: path .b does not exist; use --create-missing to allow path creation
```

Add `--create-missing` to let `nesdit` create intermediate containers:

```sh
nesdit config.json --create-missing --query '.b.c = 2'
```

**Stdout:**

```json
{"a":1,"b":{"c":2}}
```
