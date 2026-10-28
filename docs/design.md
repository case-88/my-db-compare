# Design Document

Internal design and implementation details of my-db-compare

**See also**:
- [Known Issues](known-issues.md) - Known limitations such as partition bias
- [Full Compare Mode](full-compare.md) - Design of the full comparison mode (Planned)

## Build and Development

### Directory Layout

```
my-db-compare/
├── build/                  ← Build output (binary)
├── cmd/my-db-compare/      ← Main entry point
├── internal/               ← Internal packages
│   ├── comparator/         ← Comparison logic
│   ├── config/             ← Configuration management
│   ├── db/                 ← DB connections
│   ├── reporter/           ← Result output
│   ├── runner/             ← Run orchestration
│   └── sampler/            ← Sampling logic
├── pkg/types/              ← Shared types
├── docs/                   ← Design documents
└── config.yaml             ← Example configuration file
```

### Build

```bash
# Install dependencies
make deps

# Build (output: ./build/my-db-compare)
make build

# Test
make test

# Unit tests only (no DB connection required)
go test -short ./...

# Everything (deps + fmt + vet + test + build)
make all
```

**Note**: Build output is always written to the `./build/` directory.

## Core Design Principles

1. **Sampling mode** (default) → Sample-based comparison (designed for large tables)
2. **Full mode** (optional) → Compares all data ([details](full-compare.md))
3. **Random sampling** → Samples evenly across the entire data range
4. **PK/UQ based** → The comparison key is the Primary Key (or a Unique Key if there is no PK)
5. **Parallel processing** → Tables are processed in parallel, one goroutine per table
6. **Retry mode** → Mismatches caused by replication lag are re-checked later with the separate `retry` mode, which re-reads only the rows in an earlier result ([details](retry-mode.md)). `sample` and `full` do not re-check automatically.

## Using information_schema

The tool queries `information_schema` to fetch table metadata and key information.

### Batched Metadata Queries (Optimization)

**Previous approach (N×4 queries)**: number of tables × 4 queries (PK, UK, Columns, Min/Max)
- With 550 tables: 2,200+ queries → about 2 minutes

**Current approach (3 queries)**: batched queries per schema
- With 550 tables: 3 queries → about 2-3 seconds

```sql
-- 1. Fetch Primary Key columns in bulk
SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION
FROM information_schema.KEY_COLUMN_USAGE
WHERE (TABLE_SCHEMA, TABLE_NAME) IN (('db1','t1'), ('db1','t2'), ...)
  AND CONSTRAINT_NAME = 'PRIMARY'
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION;

-- 2. Fetch Unique Key columns in bulk (for tables without a PK)
SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, COLUMN_NAME, SEQ_IN_INDEX
FROM information_schema.STATISTICS
WHERE (TABLE_SCHEMA, TABLE_NAME) IN (('db1','t1'), ('db1','t2'), ...)
  AND NON_UNIQUE = 0
  AND INDEX_NAME != 'PRIMARY'
ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX;

-- 3. Fetch column information in bulk
SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY
FROM information_schema.COLUMNS
WHERE (TABLE_SCHEMA, TABLE_NAME) IN (('db1','t1'), ('db1','t2'), ...)
ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION;
```

### Listing Tables (Single Query)

```sql
SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_ROWS, AVG_ROW_LENGTH
FROM information_schema.TABLES
WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE';
```

## Data Extraction Algorithm

### Partitioning

```
1. Fetch PK Min/Max
   SELECT MIN(pk), MAX(pk) FROM table

2. Split into M partitions
   chunk_size = (max - min) / M
   partitions = [(min, min+chunk), (min+chunk, ...), ]

3. Sample N/M rows from each partition
   SELECT * FROM table
   WHERE pk >= {partition_start}
   ORDER BY pk ASC LIMIT {N/M}
```

### Supported PK Types

The following PK types are supported for partitioning:

| Type | MySQL example | Description |
|------|------------|------|
| `int64` | INT, BIGINT | Signed integer |
| `uint64` | BIGINT UNSIGNED | Unsigned integer |
| `time.Time` | DATETIME, TIMESTAMP | Time-based split |
| `string` | VARCHAR, CHAR | Split by first character |
| `[]byte` | BINARY, VARBINARY | Converted to a string, then split |

### Choosing the Partition Count M

The number of partitions is calculated so that no query exceeds the **per-query row limit (MaxSamplesPerPart)**:

```go
// Core rule: the LIMIT query for each partition never exceeds MaxSamplesPerPart
func calculatePartitionCount(cfg PartitionConfig, estimatedRows int64) int {
    // Case 1: small table (rows <= sampleSize)
    //         → compute partitions from the table size
    if estimatedRows > 0 && estimatedRows <= sampleSize {
        return ceil(estimatedRows / maxSamplesPerPart)
    }

    // Case 2: large table (rows > sampleSize)
    //         → compute partitions from the sample size
    minNeeded := ceil(sampleSize / maxSamplesPerPart)
    return max(minNeeded, proportionalPartitions)
}
```

**Default setting**: `MaxSamplesPerPart = 1000` (LIMIT 1000 per partition)

| Table size | Sample of 30,000 | Partitions | Rows per partition query |
|------------|-------------|----------|--------------|
| 500 rows | 500 (all) | 1 | ~500 |
| 1,000 rows | 1,000 (all) | 1 | ~1,000 |
| 5,000 rows | 5,000 (all) | 5 | ~1,000 |
| 10,000 rows | 10,000 (all) | 10 | ~1,000 |
| 30,000 rows | 30,000 (all) | 30 | ~1,000 |
| 50,000+ rows | 30,000 (sample) | 30 | ~1,000 |
| 500,000+ rows | 30,000 (sample) | 300 | ~100 |

**Randomness**: The partition count is varied so that repeated runs sample different data:
- **Small table (rows ≤ sampleSize)**: no randomness (the whole table is read)
- **Unknown/Medium table**: increase of 0 to +10% only (so minNeededForLimit is never violated)
- **Large table**: ±10% variation (with minNeededForLimit as the lower bound)

## Hashing Algorithm

### xxHash (xxh3)

- Fastest among non-cryptographic hashes
- Low collision probability
- Go library: `github.com/zeebo/xxh3`

```go
func hashRow(row map[string]interface{}, columns []string) uint64 {
    var buf bytes.Buffer
    for _, col := range columns {
        buf.WriteString(fmt.Sprintf("%v|", row[col]))
    }
    return xxh3.HashString(buf.String())
}
```

## Flow Diagram

```
+------------------------------------------------------------------+
|                        PHASE 1: SAMPLING                         |
+------------------------------------------------------------------+
|                                                                  |
|  +-----------+    +-----------+    +-----------+                 |
|  |  Table A  |    |  Table B  |    |  Table C  |  ...            |
|  | goroutine |    | goroutine |    | goroutine |                 |
|  +-----+-----+    +-----+-----+    +-----+-----+                 |
|        |                |                |                       |
|        v                v                v                       |
|  +-------------------------------------------------------+       |
|  |  1. Get PK/UQ info from information_schema            |       |
|  |  2. SELECT MIN(pk), MAX(pk)                           |       |
|  |  3. Split into M partitions                           |       |
|  |  4. Sample LIMIT N/M from each partition (Source)     |       |
|  |  5. Fetch Target rows by PK list                      |       |
|  |  6. Compare with xxHash                               |       |
|  +-------------------------------------------------------+       |
|                              |                                   |
|                              v                                   |
|                    +-----------------+                           |
|                    | Mismatch log    |                           |
|                    | - Table, PK     |                           |
|                    | - Diff columns  |                           |
|                    | (written as     |                           |
|                    |  found)         |                           |
|                    +-----------------+                           |
|                                                                  |
+------------------------------------------------------------------+

Rows that differ only because the target was still catching up
(replication lag) are re-checked later with `retry` (retry-mode.md).
```

## Data Structures

```go
// Configuration
type Config struct {
    Source      DBConfig    // Source DB connection info
    Target      DBConfig    // Target DB connection info
    Comparison  Comparison  // Comparison settings
    Tables      TableFilter // Table filter
    Output      Output      // Output settings
    Mappings    []TableMapping // Source ↔ target table/column mappings
}

type Comparison struct {
    Mode                       string        // sampling / full (default: sampling)
    SampleSize                 int           // [sampling] samples per table (default: 1000)
    Parallelism                int           // number of tables processed concurrently (default: 5)
    IgnoreDatetimePrecision    bool          // compare datetimes at second precision (default: true)
    IgnoreZeroDatetime         bool          // treat 0000-00-00 00:00:00 the same as NULL (default: true)
    IgnoreJSONKeyOrder         bool          // ignore JSON key order (default: true)
    IgnoreLargeColumns         bool          // exclude large columns (default: false)
    GenerateCorrectionQueries  bool          // generate correction queries (default: false)

    // Full-mode-only settings
    FullModeChunkSize      int  // [full] chunk size (default: 2000)
    FullModeDelayMs        int  // [full] delay between chunks in ms (default: 10)
    FullModeParallelism    int  // [full] number of partitions in parallel (default: 4)
}

type TableFilter struct {
    Include []string // include patterns (database.table format, e.g. "mydb.*")
    Exclude []string // exclude patterns (database.table format, e.g. "*.tmp_*")
}

type DBConfig struct {
    Host     string
    Port     int
    User     string
    Password string
    Database string // database used for the connection (does not affect table discovery)
}

type Output struct {
    Format   string // jsonl, json, text
    Path     string // result directory (a timestamped subdirectory is created inside it automatically)
    LogLevel string // debug, info, warn, error (default: info)
}

// Table metadata
type TableMeta struct {
    Schema      string
    Name        string
    PKColumns   []string      // PK columns (order matters)
    Columns     []ColumnMeta  // all columns
    EstimatedRows int64       // estimated row count
}

type ColumnMeta struct {
    Name     string
    DataType string
    Nullable bool
}

// Comparison result
type CompareResult struct {
    Table           string
    TotalSampled    int
    MatchCount      int
    MismatchCount   int
    MissingInTarget int
    MissingInSource int
    Mismatches      []Mismatch
    Duration        time.Duration
}

type Mismatch struct {
    PK          map[string]interface{}
    Type        MismatchType  // DATA_DIFF, MISSING_TARGET, MISSING_SOURCE
    DiffColumns []string
    SourceData  map[string]interface{}
    TargetData  map[string]interface{}
    Query       string        // SELECT query for looking up the row
}

type MismatchType string

const (
    MismatchDataDiff       MismatchType = "DATA_MISMATCH"
    MismatchMissingTarget  MismatchType = "MISSING_IN_TARGET"
    MismatchMissingSource  MismatchType = "MISSING_IN_SOURCE"
)
```

## Query Patterns

### Sampling the Source

```sql
-- 1. Fetch Min/Max
SELECT MIN(pk), MAX(pk) FROM schema.table;

-- 2. Sample each partition
SELECT * FROM schema.table
WHERE pk >= ?
ORDER BY pk ASC
LIMIT ?;
```

### Verifying the Target

```sql
-- Bulk lookup by PK
SELECT * FROM schema.table
WHERE pk IN (?, ?, ?, ...);
```

### Composite PKs

```sql
-- For a composite PK (pk1, pk2)
SELECT * FROM schema.table
WHERE (pk1, pk2) >= (?, ?)
ORDER BY pk1, pk2 ASC
LIMIT ?;

-- Target lookup
SELECT * FROM schema.table
WHERE (pk1, pk2) IN ((?,?), (?,?), ...);
```

## Output Formats

### JSON Lines (.jsonl)

```json
{"timestamp":"2025-01-22T10:30:00Z","table":"mydb.member","pk":{"idx":12345},"type":"DATA_MISMATCH","diff_columns":["updated_at","status"],"source":{"updated_at":"2025-01-22 10:00:00","status":"active"},"target":{"updated_at":"2025-01-22 09:59:58","status":"active"},"query":"SELECT `idx`, `updated_at`, `status` FROM `mydb`.`member` WHERE `idx` = 12345;"}
{"timestamp":"2025-01-22T10:30:01Z","table":"mydb.member","pk":{"idx":99999},"type":"MISSING_IN_TARGET","source":{"idx":99999,"name":"test"},"query":"SELECT * FROM `mydb`.`member` WHERE `idx` = 99999;"}
```

### Text (.txt)

```
----------------------------------------
Table: mydb.member
Type:  DATA_MISMATCH
Time:  2025-01-22T10:30:00+09:00

PK:
  idx: 12345

Diff Columns: updated_at, status

Source:
  updated_at: 2025-01-22 10:00:00
  status: active

Target:
  updated_at: 2025-01-22 09:59:58
  status: inactive

Query: SELECT `idx`, `updated_at`, `status` FROM `mydb`.`member` WHERE `idx` = 12345;
----------------------------------------
```

## Performance Optimizations

### Batched Metadata Lookup

Table metadata is fetched with **batched queries**, which greatly improves performance:

| Approach | Queries | Time (550 tables) |
|------|--------|----------------------|
| Per-table queries (previous) | N×4 = 2,200 | ~2 min |
| Batched queries (current) | 3 | ~2-3 s |

See [Using information_schema](#using-information_schema) for details.

### Batched Data Queries

Target verification uses **batched queries**, which greatly improves performance:

| Approach | Queries | Description |
|------|--------|------|
| Per-row queries (previous) | 30,000 | One query per sampled row |
| Batched queries (current) | ~30 | Rows fetched 1,000 at a time in an IN clause |

```sql
-- Single PK
SELECT * FROM schema.table WHERE pk IN (?, ?, ?, ...) -- 1000 at a time

-- Composite PK
SELECT * FROM schema.table WHERE (pk1, pk2) IN ((?,?), (?,?), ...) -- 1000 at a time
```

### Excluding Large Columns

With `ignore_large_columns: true`, the following column types are excluded from comparison:

| Excluded type | Description |
|----------|------|
| TEXT | Up to 64KB |
| MEDIUMTEXT | Up to 16MB |
| LONGTEXT | Up to 4GB |
| BLOB | Up to 64KB |
| MEDIUMBLOB | Up to 16MB |
| LONGBLOB | Up to 4GB |
| JSON | Variable size |

**Benefits**:
- Less network traffic (large values are not transferred)
- Lower memory usage
- Faster comparison

**Note**: PK/UQ columns are automatically protected from exclusion.

## Considerations

| Item | Consideration |
|------|----------|
| **Composite PK** | Supports `(pk1, pk2)` tuple comparison |
| **NULL handling** | A special marker is used when hashing NULL values |
| **Type differences** | DATETIME vs TIMESTAMP normalization |
| **datetime precision** | Option to compare at second precision when comparing datetime(0) ↔ datetime(6) (`ignore_datetime_precision`) |
| **zero datetime** | Handles MySQL `0000-00-00 00:00:00` values that were converted to NULL in TiDB (`ignore_zero_datetime`) |
| **JSON key order** | Handles differences in JSON key ordering between MySQL and TiDB (`ignore_json_key_order`) |
| **Character set** | Standardized on UTF8MB4 |
| **Floating point** | Values must match exactly; there is no tolerance setting for FLOAT/DOUBLE |
| **Replication lag** | Re-check mismatches later with `retry` mode |
| **Large columns** | Option to exclude TEXT/BLOB/JSON (`ignore_large_columns`) |
| **Tables without a PK** | A UQ is used; if there is none, the table is skipped with a warning |

## Project Structure

```
my-db-compare/
├── cmd/
│   └── my-db-compare/
│       └── main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── db/
│   │   ├── connection.go
│   │   ├── connection_test.go
│   │   ├── meta.go           # information_schema queries
│   │   └── meta_test.go
│   ├── sampler/
│   │   ├── partition.go
│   │   ├── partition_test.go
│   │   ├── sampler.go
│   │   └── sampler_test.go
│   ├── comparator/
│   │   ├── hash.go
│   │   ├── hash_test.go
│   │   ├── compare.go
│   │   └── compare_test.go
│   └── reporter/
│       ├── logger.go
│       ├── logger_test.go
│       ├── summary.go
│       └── summary_test.go
├── pkg/
│   └── types/
│       └── types.go          # Shared type definitions
├── docs/
│   └── design.md             # Design document (this file)
├── testdata/
│   └── fixtures/
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## Progress Reporting

While a comparison is running, overall progress is logged once per minute. The 📊 emoji makes these lines easy to spot:

### Sampling Mode

```json
{"level":"info","msg":"📊 progress report","elapsed":"1m0s","tables":"45/120","sampled":45000,"matched":44980,"verified_mismatches":5}
{"level":"info","msg":"📊 progress report","elapsed":"2m0s","tables":"92/120","sampled":92000,"matched":91950,"verified_mismatches":12}
```

| Field | Description |
|------|------|
| `elapsed` | Elapsed time |
| `tables` | Completed tables / total tables |
| `sampled` | Total rows sampled |
| `matched` | Total rows that matched |
| `verified_mismatches` | Number of verified mismatches |

### Full Mode

```json
{"level":"info","msg":"📊 full mode progress","elapsed":"5m0s","tables":"2/10","table":"mydb.orders","chunks":"150/300","rows_compared":300000,"mismatches":5}
```

| Field | Description |
|------|------|
| `elapsed` | Elapsed time |
| `tables` | Completed tables / total tables |
| `table` | Table currently being compared |
| `chunks` | Chunk progress for the current table |
| `rows_compared` | Total rows compared |
| `mismatches` | Number of mismatches found |
