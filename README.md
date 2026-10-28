# my-db-compare

**Verify that two MySQL-compatible databases hold the same data.**

`my-db-compare` reads rows from a *source* database and a *target* database, compares them row by row, and writes every difference it finds to a log you can inspect, re-check, or turn into fix-up SQL.

It was built for database migrations between MySQL-compatible databases, where you want proof that the copy matches the original before you switch traffic over.

```
        source DB                       target DB
 (MySQL, Aurora, TiDB, ...)      (MySQL, Aurora, TiDB, ...)
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
  - `sample`: compares random rows within primary-key ranges. This is a spot check, not a proof of equality.
  - `full`: compares every source row, chunk by chunk, with bounded memory. Enable `check_target_only_rows` to also detect extra target rows.
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
| `--check-target-only-rows` | `false` | sample, full | Also check target keys for rows absent from the source (`true` / `false`) |

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

By default a source table is compared with the target table **of the same name**, column by column **with the same names**. When the target differs, describe how the two line up with `mappings`. Each entry can do three things, and you can use any of them alone or together:

| Key | Use it when | Example |
|-----|-------------|---------|
| `target` | The table was renamed or moved to another database | `target: app_v2.members` |
| `columns` | Some columns were renamed | `name: full_name` |
| `exclude` | Some source columns should not be compared (dropped on the target, or expected to differ) | `exclude: [phone, fax]` |

```yaml
mappings:
  - source: app.users          # table on the source
    target: app_v2.members     # table it was copied to on the target
    columns:                   # source column: target column
      id: member_id
      name: full_name
    exclude: [phone]           # source columns left out of the comparison

  - source: shop.orders
    target: shop.orders_new    # only the table was renamed

  - source: shop.payments      # same table name on both sides:
    target: shop.payments      # only some columns are skipped
    exclude: [legacy_memo]
```

```
 source  app.users                    target  app_v2.members
 ┌─────────────────────┐              ┌─────────────────────┐
 │ id     (PK)         │ ───────────▶ │ member_id   (PK)    │   listed in columns
 │ name                │ ───────────▶ │ full_name           │   listed in columns
 │ email               │ ───────────▶ │ email               │   not listed → same name
 │ phone               │      ✕       │                     │   in exclude → not compared
 │                     │              │ created_by          │   target-only → ignored
 └─────────────────────┘              └─────────────────────┘
```

#### Rules

| Rule | Details |
|------|---------|
| **Unlisted columns keep their name** | List only the columns that were renamed. |
| **Key columns follow the same mapping** | Rows are matched by the **source** table's primary key (or unique key, if it has no primary key). If the key was renamed, list it in `columns` like any other column. |
| **Every compared source column must exist on the target** | A source column with no counterpart on the target is an error. If it was dropped on purpose, put it in `exclude`. |
| **Target-only columns need nothing** | Only source columns are read, so extra columns on the target are never read or compared. Don't list them anywhere. |
| **`exclude` takes source column names** | Write each name as it is on the source. An excluded column needs no `columns` entry, and a column cannot be in both. |
| **Key columns cannot be excluded** | Rows are matched by the key, so excluding a key column is an error. |
| **Mapped tables are always compared** | Even when `tables.include` / `exclude` would skip them. Write each source table once. |
| **Source names ignore case** | `source` and the column keys are matched to the source table and columns case-insensitively. The `target` table name is used exactly as written, so write it with the target's real case. |
| **Typos stop the run** | Before comparing in `sample` and `full` mode, the tool checks every mapped table and column on both sides, including the names in `exclude`, and exits with an error that names the problem. Without this check a typo would look like every row is missing from the target, or would silently skip a column. |

#### Excluding columns safely

`exclude` is the only way a column is silently left out of the comparison, so differences in that column will **never** be reported. To keep that visible:

- Excluded columns are listed in the log for every table (`excluded columns from comparison`), and in `--dry-run` as `app.users -> app_v2.members (excluding phone)`.
- In `sample` and `full` mode, a name in `exclude` that does not exist on the source stops the run, so a typo cannot hide anything. `retry` does not re-check names; run it with the config you compared with.
- Prefer excluding only columns that are truly gone or meant to differ. If you only want to skip large `TEXT`/`BLOB`/`JSON` columns everywhere, use `comparison.ignore_large_columns` instead.

Error messages you may see:

| Message | Meaning |
|---------|---------|
| `target column app_v2.members.phone not found (for source column phone)` | The column is not on the target. Map it in `columns`, or `exclude` it. |
| `excluded column "phnoe" not found in the source table` | A typo in `exclude`. |
| `key column "id" cannot be excluded; rows are matched by it` | Remove the key column from `exclude`. If it was renamed, map it in `columns`. |
| `column "name" is in both columns and exclude` | Keep it in one of them. |

#### What you get in the results

Results always use **source** names, so mapped and unmapped tables read the same. Queries that run on the target use **target** names:

| Field | Uses | Example for the mapping above |
|-------|------|-------------------------------|
| `table`, `pk`, `source`, `target`, `diff_columns` | source names | `"pk": {"id": 7}`, `"diff_columns": ["name"]` |
| `query` | source names, runs on the source | ``SELECT `id`, `name` FROM `app`.`users` WHERE `id` = 7;`` |
| `target_query` | target names, aliased back to source names | ``SELECT `member_id` AS `id`, `full_name` AS `name` FROM `app_v2`.`members` WHERE `member_id` = 7;`` |
| `correction_query` | target names | ``UPDATE `app_v2`.`members` SET `full_name` = 'Kim' WHERE `member_id` = 7;`` |

`target_query` appears only for mapped tables.

#### Mappings and retry

`retry` re-reads each mismatch with `query` on the source and `target_query` on the target. Run it with **the same `mappings`** used to produce the results. Columns in `exclude` are dropped before re-comparing, even if an older result's query selected them. If the config and a result file disagree about whether a table is mapped, those records are kept as they were (never reported as resolved), and a warning is logged.

### Checking rows that exist only on the target

By default, comparison reads source rows and looks up their keys on the target. It does **not** detect extra target rows. Enable the optional reverse check:

```yaml
comparison:
  check_target_only_rows: true
  generate_correction_queries: true
```

Or use `--check-target-only-rows=true` (use `false` to override an enabled config).

- `full` scans all target keys and checks their existence on the source, including an empty source and keys outside the source's range.
- `sample` draws an independent target key sample. Unsampled extra rows may remain undetected.
- The extra pass reads keys only and adds source lookups. Common rows are not compared or counted twice.
- Extra target rows become `MISSING_IN_SOURCE`. With correction queries enabled, they receive a `DELETE` using the mapped target table and column names, with the **entire** key in `WHERE`. SQL is generated only, never executed.
- This applies to rows in selected table pairs, not discovery of target-only tables. `retry` rechecks existing records without another scan.

The summary records `config.check_target_only_rows`. Each table records whether its check completed (`target_only_checked`) and how many target keys it examined (`target_rows_checked`).

## Choosing a mode

| | `sample` | `full` | `retry` |
|---|---|---|---|
| **Use it for** | Routine checks, quick confidence | Final sign-off after a migration | Confirming earlier mismatches |
| **Rows compared** | Random rows within PK ranges, up to the requested sample | All source rows; optionally all target keys | Only the rows listed in a previous run |
| **Typical duration** | Minutes | Hours to days, depending on size | Seconds to minutes |
| **Memory** | Grows with the sample size | Fixed, based on chunk size | Small |

### How full mode works

1. Each table is split into ranges of its primary key.
2. Each range is read in chunks of `full_mode_chunk_size` rows from both databases, and the chunks are compared.
3. Every mismatch is written to the log immediately, so memory stays flat.
4. Progress is logged every minute.

Pagination sorts by the complete key and resumes strictly after the last complete key. Composite keys have no fixed column-count limit; strings and maximum integers do not require incrementing a value.

Sampling uses `ORDER BY RAND()` within each range (or across a small table). This removes the preference for the first rows but scans and sorts eligible rows even with a small `LIMIT`. It is not a uniform sample across an unevenly populated table, and range quotas may leave the requested sample size unmet.

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
  "query": "SELECT `id`, `status`, `updated_at` FROM `app`.`users` WHERE `id` = 123;",
  "correction_query": "UPDATE `app`.`users` SET `status` = 'active', `updated_at` = '2026-01-01 10:00:00' WHERE `id` = 123;",
  "timestamp": "2026-01-01T12:00:45Z"
}
```

| Field | Meaning |
|-------|---------|
| `type` | `DATA_MISMATCH` (row exists in both but differs), `MISSING_IN_TARGET`, or `MISSING_IN_SOURCE` |
| `diff_columns` | Columns whose values differ |
| `query` | A `SELECT` on the source that reproduces the row (key and differing columns), for manual checking |
| `target_query` | The same `SELECT` for the target, written with target names. Present only for [mapped tables](#mapping-tables-and-columns) |
| `correction_query` | SQL that makes the target match the source, using the target's table and column names. Present only when `generate_correction_queries` is `true`. **Review it before running it.** |

`summary.json` also records `run_mode` (`sample`, `full` or `retry`) and, for retry runs, the `input_dir` that was re-checked.

`status` distinguishes `RUNNING`, `INCOMPLETE`, `MISMATCHES FOUND`, `SOURCE ROWS MATCHED`, `SAMPLED ROWS MATCHED`, `RETRY ROWS MATCHED`, and `ALL MATCHED`. `ALL MATCHED` requires a completed full comparison with the target-only check enabled. When disabled, the console prints `Target-only rows: NOT CHECKED`.

Query/partition/output failures are propagated. Failed tables count toward `tables_failed`, retain their `error`, and are excluded from `tables_compared`. Skipped tables also make the run incomplete. Exit codes: `0` no detected differences within the stated scope; `1` mismatches; `2` errors/incomplete comparison; `124` timeout; `130` cancellation.

Backslashes, control bytes, and non-ASCII bytes use hexadecimal SQL literals in generated queries, independent of `NO_BACKSLASH_ESCAPES`.

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

Correctness regression tests use a disposable local MySQL server: set `TEST_COMPARE_PORT` to its loopback port and run `go test ./...`. They connect as `root` without a password, create isolated `compare_test_*` schemas, and remove them afterwards. `go test -short ./...` also skips these tests.

More documentation:

- [docs/design.md](docs/design.md): internal architecture
- [docs/full-compare.md](docs/full-compare.md): full comparison mode
- [docs/retry-mode.md](docs/retry-mode.md): retry mode
- [docs/known-issues.md](docs/known-issues.md): known limitations

## License

MIT. See [LICENSE](LICENSE).
