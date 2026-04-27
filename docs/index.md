# nesdit

Edit structured config (JSON/YAML/TOML) with jq-style queries.

## Installation

```sh
go install github.com/sdkks/nesdit/cmd/nesdit@latest
```

## Quick start

```sh
# Print a YAML file after editing .replicas
nesdit deployment.yaml --query '.replicas = 3'

# Edit in-place (atomic write)
nesdit -i config.json --query '.env = "production"'

# Preview changes without writing
nesdit -n config.yaml --query '.timeout = "30s"'

# Check if a file is up-to-date (exit 2 on drift)
nesdit --check config.yaml --query '.'

# Open $EDITOR and get a suggested query from your edits
nesdit --edit config.yaml
```

## Supported formats

| Format | Extensions | Notes |
|--------|------------|-------|
| JSON   | `.json`    | Preserves key order |
| YAML   | `.yaml`, `.yml` | Single-document per file |
| TOML   | `.toml`    | Tables and arrays of tables |

Format is detected from the file extension. Use `--format` to override.

## Flags

See the [CLI reference](reference/nesdit.md) for all flags.

## Resource limits

By default nesdit caps inputs at 10 MiB (`--max-bytes`), nesting at 1000 levels (`--max-depth`), YAML nodes at 100 000 (`--max-yaml-nodes`), and query files at 1 MiB (`--max-query-bytes`). Pass `0` to any cap to disable it — for example `--max-bytes=0` handles large Terraform state files.
