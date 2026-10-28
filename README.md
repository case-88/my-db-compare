# my-db-compare

**Verify that two MySQL-compatible databases hold the same data.**

`my-db-compare` reads rows from a *source* database and a *target* database, compares them row by row, and writes every difference it finds to a log you can inspect, re-check, or turn into fix-up SQL.

It was built for database migrations (for example, MySQL/Aurora → TiDB), where you want proof that the copy matches the original before you switch traffic over.

```
   source DB                      target DB
  (MySQL/Aurora)                   (TiDB, ...)
        │                              │
        └──────────┐        ┌──────────┘
                   ▼        ▼
              ┌──────────────────┐
              │  my-db-compare   │   sample → quick spot check
              │                  │   full   → every row
              └────────┬─────────┘   retry  → re-check past mismatches
                       ▼
          results/<timestamp>/
            ├── mismatch-<db>.<table>.jsonl
            └── summary.json
```

## Features

- **Three run modes**
  - `sample`: compares a random sample of rows per table. Takes minutes, so it suits routine monitoring.
  - `full`: compares every row, chunk by chunk, with bounded memory. Use it for final sign-off after a migration.
  - `retry`: re-checks only the rows that mismatched in an earlier run. Useful when the target was still catching up (replication lag).
- **Parallel by design.** Several tables run at once, and in full mode each table is also split into primary-key ranges.
- **Migration-aware comparison.** Optional rules ignore differences that are expected between engines:
  - datetime microseconds
  - `0000-00-00 00:00:00` vs `NULL`
  - JSON key order
- **Actionable output.** Each mismatch includes the primary key, the differing columns, both row values, a `SELECT` to reproduce it, and optionally a correction query (`UPDATE` / `INSERT` / `DELETE`).
- **Flexible table filters** with wildcards, such as `app.*`, `*.users` or `*.*_backup`.
- **Table and column mapping** for targets where tables or columns were renamed.

## Supported databases

Any MySQL wire-compatible server:

- MySQL 5.7 / 8.0+
- Amazon Aurora MySQL
- TiDB
- MariaDB

## Requirements

- Go 1.25 or later, to build from source
- A database user with `SELECT` on the tables to compare and on `information_schema` (a read-only account is enough)

## Installation

```bash
git clone https://github.com/case-88/my-db-compare.git
cd my-db-compare
make deps
make build        # → ./build/my-db-compare
```

To cross-compile for linux/darwin on amd64 and arm64:

```bash
make build-all
```

## Quick start

**1. Create your config.** [`config.yaml`](config.yaml) is a fully commented example of every option. Copy it and fill in your connection details:

```bash
cp config.yaml config.local.yaml     # config.local.yaml is git-ignored
vi config.local.yaml
```

Keep passwords out of the file by referencing environment variables:

```yaml
source:
  password: ${SOURCE_DB_PASSWORD}
```

```bash
export SOURCE_DB_PASSWORD='...'
export TARGET_DB_PASSWORD='...'
```

**2. Preview which tables will be compared.** Nothing is compared yet:

```bash
./build/my-db-compare sample --config config.local.yaml --dry-run
```

**3. Run a sampling comparison:**

```bash
./build/my-db-compare sample --config config.local.yaml
```

**4. Read the results** in `./results/<timestamp>/`. Start with `summary.json`.

## Usage

```bash
my-db-compare <sample|full|retry> [flags]
```

### Examples

```bash
# Sample only specific tables
my-db-compare sample --config config.local.yaml --tables "app.users,app.orders"

# Bigger sample, more tables in parallel
my-db-compare sample --config config.local.yaml --sample-size 5000 --parallelism 10

# Compare every row, gently: small chunks with a pause between them
my-db-compare full --config config.local.yaml --chunk-size 1000 --delay-ms 50 --parallelism 2

# Re-check the mismatches from a previous run
my-db-compare retry --config config.local.yaml --input ./results/2026-01-01T12:00:00

# Stop after 30 minutes, with verbose logs
my-db-compare full --config config.local.yaml --timeout 30m --log-level debug
```

### Flags

Flags override values in the config file.

| Flag | Default | Modes | Description |
|------|---------|-------|-------------|
| `--config` | `config.yaml` | all | Path to the config file |
| `--output` | `./output` | all | Directory for output files |
| `--log-level` | `info` | all | `debug`, `info`, `warn` or `error` |
| `--timeout` | none | all | Overall time limit, e.g. `30m` or `2h` |
| `--dry-run` | `false` | all | List the tables that would be compared, then exit |
| `--version` | | all | Print version information |
| `--tables` | from config | sample, full | Comma-separated tables to compare, e.g. `db1.users,db2.*` |
| `--sample-size` | from config | sample, full | Rows sampled per table |
| `--parallelism` | from config | sample, full | Tables processed at the same time |
| `--chunk-size` | from config | sample, full | Full mode: rows read per query |
| `--delay-ms` | from config | sample, full | Full mode: pause between chunks, in ms |
| `--input` | | retry | **Required.** Result directory from an earlier run |
| `--ignore-datetime-precision` | `true` | all | Compare datetimes to the second |
| `--ignore-zero-datetime` | `true` | all | Treat `0000-00-00 00:00:00` as `NULL` |
| `--ignore-json-key-order` | `true` | all | Compare JSON by meaning, not by key order |
| `--ignore-large-columns` | `false` | all | Skip `TEXT` / `BLOB` / `JSON` columns |
| `--generate-correction-queries` | `false` | all | Write SQL that would make the target match the source |

## Configuration

See [`config.yaml`](config.yaml) for every option, its default, and an explanation. The main sections are:

| Section | What it controls |
|---------|------------------|
| `source`, `target` | Connection details. `database` is only used to connect. It does **not** choose which tables are compared. |
| `comparison` | Mode, sample size, parallelism, comparison rules and full-mode tuning |
| `tables` | `include` / `exclude` patterns. An empty `include` means every non-system database. |
| `output` | Output format (`jsonl`, `json`, `text`), directory and log level |
| `mappings` | Source ↔ target table and column name mapping (see below) |

Any setting can also come from an environment variable with the `DBCOMPARE_` prefix, for example `DBCOMPARE_SOURCE_HOST` or `DBCOMPARE_COMPARISON_SAMPLE_SIZE`.

### Table filter patterns

Patterns are always written as `database.table`, and `*` matches any text.

| Pattern | Matches |
|---------|---------|
| `app.*` | Every table in the `app` database |
| `*.users` | The `users` table in every database |
| `app.order*` | Tables in `app` whose names start with `order` |
| `shop_*.*` | Every table in databases whose names start with `shop_` |
| `*.*_backup` | Tables whose names end with `_backup` |

A table is compared when it matches at least one `include` pattern and no `exclude` pattern.

### Mapping tables and columns

When a table or its columns were renamed on the target, map them in `mappings`:

```yaml
mappings:
  - source: app.users
    target: app_v2.members
    columns:              # source column: target column
      id: member_id
      name: full_name
```

- Columns you don't list are assumed to have the **same name** on both sides.
- Key columns follow the same rule. If the primary key was renamed, list it in `columns`.
- Rows are matched by the **source** table's primary key (or unique key).
- Mapped tables are always compared, even if `tables.include` / `exclude` would skip them.
- Before comparing, the tool checks that every mapped table and column exists, and stops with an error if one does not.
- Results use the source table and column names.

> **Current limits:** mappings work in `sample` mode only, and cannot be combined with `generate_correction_queries`.

## Choosing a mode

| | `sample` | `full` | `retry` |
|---|---|---|---|
| **Use it for** | Routine checks, quick confidence | Final sign-off after a migration | Confirming earlier mismatches |
| **Rows compared** | `sample_size` random rows per table | All rows | Only the rows listed in a previous run |
| **Typical duration** | Minutes | Hours to days, depending on size | Seconds to minutes |
| **Memory** | Grows with the sample size | Fixed, based on chunk size | Small |

### How full mode works

1. Each table is split into ranges of its primary key.
2. Each range is read in chunks of `full_mode_chunk_size` rows from both databases, and the chunks are compared.
3. Every mismatch is written to the log immediately, so memory stays flat.
4. Progress is logged every minute.

**Watch your connection count.** Full mode opens many connections at once:

```
connections = parallelism × full_mode_parallelism × 2 (source + target)

e.g. parallelism = 2, full_mode_parallelism = 4  →  16 connections
```

On a busy production database, start with low parallelism and a non-zero `full_mode_delay_ms`.

Design details are in [docs/full-compare.md](docs/full-compare.md).

## Output

Each run creates a timestamped directory under `output.path`:

```
results/
└── 2026-01-01T12:00:00/
    ├── mismatch-app.users.jsonl     # one line per mismatched row
    ├── mismatch-app.orders.jsonl
    └── summary.json                 # totals per table, run mode, timing
```

### Mismatch record

```json
{
  "table": "app.users",
  "pk": {"id": 123},
  "type": "DATA_MISMATCH",
  "diff_columns": ["status", "updated_at"],
  "source": {"id": 123, "status": "active",   "updated_at": "2026-01-01 10:00:00"},
  "target": {"id": 123, "status": "inactive", "updated_at": "2026-01-01 09:00:00"},
  "query": "SELECT * FROM `app`.`users` WHERE `id` = 123;",
  "correction_query": "UPDATE `app`.`users` SET `status` = 'active', `updated_at` = '2026-01-01 10:00:00' WHERE `id` = 123;",
  "timestamp": "2026-01-01T12:00:45Z"
}
```

| Field | Meaning |
|-------|---------|
| `type` | `DATA_MISMATCH` (row exists in both but differs), `MISSING_IN_TARGET`, or `MISSING_IN_SOURCE` |
| `diff_columns` | Columns whose values differ |
| `query` | A `SELECT` that reproduces the row, for manual checking |
| `correction_query` | SQL that makes the target match the source. Present only when `generate_correction_queries` is `true`. **Review it before running it.** |

`summary.json` also records `run_mode` (`sample`, `full` or `retry`) and, for retry runs, the `input_dir` that was re-checked.

## Development

```bash
make test             # all tests (integration tests skip unless a DB is configured)
make test-coverage    # HTML coverage report
go test -short ./...  # unit tests only
```

Integration tests need real databases. Point them at your own servers with environment variables:

```bash
export TEST_SOURCE_HOST=127.0.0.1 TEST_SOURCE_PORT=3306 TEST_SOURCE_USER=root TEST_SOURCE_PASSWORD=...
export TEST_TARGET_HOST=127.0.0.1 TEST_TARGET_PORT=4000 TEST_TARGET_USER=root TEST_TARGET_PASSWORD=...
go test ./...
```

When these variables are not set, the integration tests are skipped.

More documentation:

- [docs/design.md](docs/design.md): internal architecture
- [docs/full-compare.md](docs/full-compare.md): full comparison mode
- [docs/retry-mode.md](docs/retry-mode.md): retry mode
- [docs/known-issues.md](docs/known-issues.md): known limitations

## License

MIT
