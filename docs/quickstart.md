# Quickstart

This guide takes you from zero to a working `nesdit` edit in under 5 minutes. It uses only JSON and YAML — the two most common formats. No prior knowledge of other formats is needed.

## 1. Install nesdit

```sh
go install github.com/sdkks/nesdit/cmd/nesdit@latest
```

See [Installation](install.md) for other options.

## 2. Edit a JSON file to stdout

Create a sample file:

```sh
cat > app.json << 'EOF'
{"env":"staging","replicas":1}
EOF
```

Print the file with `.env` changed to `"production"`:

```sh
nesdit app.json --query '.env = "production"'
```

Expected output:

```json
{"env":"production","replicas":1}
```

The file on disk is **not** changed. `nesdit` printed the result to stdout.

## 3. Edit a file in-place

To write the change back to disk, add `-i`:

```sh
nesdit -i app.json --query '.env = "production"'
```

The write is atomic (temp file + rename), so the file is never left in a partial state. Verify:

```sh
nesdit app.json --query '.env'
```

Expected output:

```
"production"
```

## 4. Preview changes with --dry-run

Before committing an edit, preview the diff:

```sh
nesdit app.json -n --query '.replicas = 3'
```

Expected output (unified diff):

```diff
--- app.json
+++ app.json
@@ -1 +1 @@
-{"env":"production","replicas":1}
+{"env":"production","replicas":3}
```

The file is **not** written. `--dry-run` (or `-n`) always exits `0` even when the diff is non-empty.

## 5. Check for drift with --check

`--check` tests whether a query would change the file, without writing anything:

```sh
# No drift — identity query
nesdit app.json --check --query '.'
echo "exit: $?"
```

Expected: exits `0`, no output.

```sh
# Drift detected
nesdit app.json --check --query '.replicas = 99'
echo "exit: $?"
```

Expected: exits `2`, no output.

!!! tip
    Exit code `2` means "the file would change." Exit code `1` means an error (parse failure, bad flag, etc.). Use `--check` in pre-commit hooks and CI to gate on config drift.

## 6. Edit a YAML file

The same flags work for YAML:

```sh
cat > deploy.yaml << 'EOF'
name: myapp
replicas: 1
image: nginx
EOF
```

```sh
nesdit -i deploy.yaml --query '.replicas = 3'
```

Verify:

```sh
nesdit deploy.yaml --query '.replicas'
```

Expected output:

```
3
```

## What's next

- [Modes](modes/in-place.md) — all run modes in detail: in-place, stdout, STDIN stream, dry-run, check, and expression builder.
- [Format examples](examples/formats/index.md) — all six format round-trip combinations including cross-format transcoding with `--output-format`.
- [Expression examples](examples/expressions/index.md) — `--arg`, `--argjson`, `--from-file`, `--where`, and more.
- [Pipeline patterns](examples/pipelines/index.md) — glob batches, CI steps, pre-commit hooks.
- [Reference](reference/nesdit.md) — all flags.
