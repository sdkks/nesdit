# Expression builder (`--edit`)

`--edit` opens `$EDITOR` on a temporary copy of the file. When you save and quit, `nesdit` diffs the before and after versions and prints:

1. A suggested invocation — the `nesdit` command you can paste into a script or CI step.
2. The underlying query alone — useful with `-f`/`--from-file`.
3. A preview of the transformed document in the source format.

`--edit` always writes to stdout. It never modifies the source file.

## Requirements

- `$EDITOR` must be set (or `$VISUAL` as a fallback, then `vi`).
- The terminal must be an interactive TTY. `--edit` rejects non-TTY invocations with a clear error.

## Basic usage

```sh
nesdit --edit deploy.yaml
```

`nesdit` opens your editor on a copy of `deploy.yaml`. Change `replicas: 3` to `replicas: 5`, save, and quit.

**Output:**

```
## Suggested command:

nesdit deploy.yaml --query '.replicas = 5'

## Query:

.replicas = 5

# yaml
replicas: 5
image: nginx
```

Sections are separated so that `grep -A1 'Suggested command'` extracts the command and `# yaml` is a language hint for terminal syntax highlighters.

## No change detected

If you quit the editor without modifying the file, `nesdit` exits `0` and reports:

```
no change detected
```

## $EDITOR not set

If `$EDITOR` is unset, `nesdit` falls back to `$VISUAL`, then `vi`. If none is available, it exits `1` with guidance:

```
nesdit: error: flag.invalid: $EDITOR is not set; set $EDITOR to your preferred editor or use --query for non-interactive edits
```

## Editor exits non-zero

If your editor exits non-zero (e.g., `:cq` in Vim to quit without saving), `nesdit` treats it as a user cancel and exits `0` with a stderr notice:

```
nesdit: warn: flag.precedence: editor exited non-zero, no output produced
```

## Flag conflicts

`--edit` is an interactive authoring mode and is mutually exclusive with all other mode flags:

| Combination | Error |
|---|---|
| `--edit -i` | `nesdit: error: flag.conflict: --edit and -i are mutually exclusive: --edit always prints to stdout; use the emitted query with -i in a second invocation` |
| `--edit --dry-run` | `nesdit: error: flag.conflict: --edit is interactive and emits its own preview; --dry-run is not compatible` |
| `--edit --check` | `nesdit: error: flag.conflict: --edit is interactive; --check is for non-interactive drift detection` |

!!! note
    `-e` is **not** a short flag for `--edit`. In `jq`, `-e` means `--exit-status`. To avoid confusing pipelines, `nesdit` reserves `-e` and rejects it with an "unknown flag" error. Always use `--edit`.

## Workflow: build a reproducible query

1. Run `nesdit --edit values.yaml` and make the desired change in your editor.
2. Copy the **Suggested command** from the output.
3. Paste it into your Makefile, CI workflow, or script.
4. On the next run, use the `-i` flag to apply it in-place:

```sh
nesdit -i values.yaml --query '.replicas = 5'
```

## Related flags

- [`--query`](../reference/nesdit.md) — provide the query directly (non-interactive).
- [`-f` / `--from-file`](../reference/nesdit.md) — load the query from a `.jq` file.
- [`-i` / `--in-place`](in-place.md) — apply the generated query back to the source file.
