# Full Compare Mode

Full mode scans source rows by default. Set `comparison.check_target_only_rows: true`
to add a target-key scan, including an empty source and keys outside its range.
Only target-only rows are reported by the extra pass. With correction queries
enabled, they receive a mapped `DELETE` with the complete key; SQL is never executed.

Pagination resumes after the complete last key using conditions such as
`a > ? OR (a = ? AND b > ?)`, extended to all key columns. `ORDER BY` uses the same
complete key. Query/output errors and cancellation produce an incomplete result
and a nonzero exit code.

Design document for the full data comparison feature.

**Status**: Implemented

## Overview

| Item | Sampling mode | Full mode |
|------|--------------|-----------|
| Purpose | Sample-based verification of large tables | Exhaustive data consistency check |
| Comparison scope | Sampled rows | All source rows; target-only keys when enabled |
| Typical usage | Routine monitoring | Manual / infrequent runs |
| Duration | Minutes | Hours to days |

## Use Cases

- Verifying full data consistency after a migration
- Exhaustively checking tables suspected of mismatches
- Fast full verification during maintenance windows or service downtime

## Configuration

```yaml
comparison:
  mode: full                     # sampling / full (default: sampling)
  check_target_only_rows: false   # optional reverse key scan
  parallelism: 10                # Number of tables processed in parallel (shared by both modes)

  # sampling mode only
  sample_size: 1000

  # full mode only
  full_mode_chunk_size: 2000     # Chunk size (default: 2000)
  full_mode_delay_ms: 10         # Delay between chunks (default: 10ms)
  full_mode_parallelism: 4       # Number of parallel partitions within a table (default: 4)
```

### Configuration Reference

| Key | Default | Description |
|------|--------|------|
| `mode` | `sampling` | Selects the comparison mode |
| `check_target_only_rows` | `false` | Scan target keys for rows missing from source |
| `parallelism` | `10` | Number of tables processed concurrently |
| `full_mode_chunk_size` | `2000` | Chunk (batch) size |
| `full_mode_delay_ms` | `10` | Wait time between chunks (spreads DB load) |
| `full_mode_parallelism` | `4` | Number of partitions processed in parallel within a table |

## Architecture

### Concurrent Connections

```
concurrent connections = parallelism × full_mode_parallelism × 2 (source/target)

e.g. parallelism=2, full_mode_parallelism=4
    → 2 × 4 × 2 = 16 connections
```

### Parallel Processing Layout

```
parallelism: 2, full_mode_parallelism: 4

┌─ Table 1 ─────────────────────────┐  ┌─ Table 2 ─────────────────────────┐
│ Partition 1 → chunk → chunk → ... │  │ Partition 1 → chunk → chunk → ... │
│ Partition 2 → chunk → chunk → ... │  │ Partition 2 → chunk → chunk → ... │
│ Partition 3 → chunk → chunk → ... │  │ Partition 3 → chunk → chunk → ... │
│ Partition 4 → chunk → chunk → ... │  │ Partition 4 → chunk → chunk → ... │
└───────────────────────────────────┘  └───────────────────────────────────┘
         (run concurrently)                     (run concurrently)
```

### Partitioning

Partitions are split by PK range:

```
Table: 100 million rows, full_mode_parallelism: 4

Partition 1: PK 1 ~ 25,000,000           → Worker 1
Partition 2: PK 25,000,001 ~ 50,000,000  → Worker 2
Partition 3: PK 50,000,001 ~ 75,000,000  → Worker 3
Partition 4: PK 75,000,001 ~ 100,000,000 → Worker 4

Inside each worker: processed sequentially, chunk_size (2000 rows by default) at a time
```

## Processing Flow

### 1. Chunk-by-Chunk Comparison

```
┌────────────────────────────────────────────────────────┐
│  Chunk comparison loop (inside each partition)         │
│                                                        │
│  for each chunk in partition:                          │
│      1. Fetch chunk_size rows from Source (PK order)   │
│      2. Fetch the same PK list from Target             │
│      3. Compare                                        │
│      4. Write mismatches to the JSONL log immediately  │
│      5. Release memory                                 │
│      6. Wait for delay_ms                              │
└────────────────────────────────────────────────────────┘
```

### 2. Immediate Logging + Memory Release

```go
// Write right after comparing each chunk
for _, chunk := range chunks {
    mismatches := compareChunk(chunk)

    // Write immediately (do not accumulate in memory)
    logger.WriteBatch(mismatches)

    // Release memory
    mismatches = nil

    // Wait to spread the load
    time.Sleep(delayMs)
}
```

**Effect**: memory usage stays roughly fixed at `chunk_size × full_mode_parallelism`.

### 3. Progress Reporting

Progress is printed every minute:

```
[mydb.users] In progress: 45,000,000 / 120,000,000 (37.5%) | Mismatches: 123 | Elapsed: 2h 15m | Est. remaining: 4h 30m
```

| Field | Description |
|------|------|
| Rows processed | Rows compared so far / total rows |
| Progress | Percentage |
| Mismatches | Number of mismatches found so far |
| Elapsed | Time since comparison of this table started |
| Est. remaining | Estimated remaining time at the current rate |

## Query Patterns

### Source Chunk Query

```sql
-- PK range-based chunk query
SELECT * FROM schema.table
WHERE pk >= ? AND pk < ?
ORDER BY pk ASC
LIMIT ?;
```

### Target Verification

```sql
-- Bulk lookup by PK (same as sampling mode)
SELECT * FROM schema.table
WHERE pk IN (?, ?, ?, ...);
```

## Load Control

### 1. Delay Between Chunks

```yaml
full_mode_delay_ms: 10  # Wait 10ms after each chunk
```

- Spreads out DB load caused by back-to-back queries
- Larger values are slower but reduce load

### 2. Chunk Size

```yaml
full_mode_chunk_size: 2000  # Process 2000 rows at a time
```

- Smaller: saves memory, more queries
- Larger: fewer queries, more memory
- Recommended: 1,000 ~ 10,000

### 3. Parallelism

```yaml
parallelism: 2              # 2 tables at once
full_mode_parallelism: 4    # 4 partitions in parallel per table
```

- During maintenance (service down): high values (fast verification)
- During live operation: low values (minimize load)

## Estimated Duration

### Formula

```
total time ≈ (total rows / chunk_size) × (query time + delay_ms) / full_mode_parallelism
```

### Examples (100-million-row table)

| Settings | Calculation | Estimated time |
|------|------|----------|
| chunk=5000, delay=10ms, parallel=4 | (100M/5000) × (50ms+10ms) / 4 | ~5 hours |
| chunk=10000, delay=10ms, parallel=4 | (100M/10000) × (80ms+10ms) / 4 | ~3.75 hours |
| chunk=5000, delay=100ms, parallel=1 | (100M/5000) × (50ms+100ms) / 1 | ~42 hours |

*Query time varies by environment.*

## Limitations

### 1. No Pause/Resume

- An interrupted run must restart from the beginning
- Reason: data can change while the comparison is running
- The last processed PK is recorded in the log (for manual inspection)

### 2. Caution with Large Tables

- Tables with hundreds of millions of rows can take hours to days
- Running one table at a time is recommended
- Setting `parallelism: 1` for sequential processing is recommended

### 3. DB Load

- Set `full_mode_delay_ms` appropriately
- Use low parallelism when running against a live system
- Monitoring is essential (CPU, connections, IOPS)

## Sampling vs Full

| Item | Sampling | Full |
|------|----------|------|
| Comparison scope | sample_size rows | Everything |
| Memory | Proportional to sample count | Proportional to chunk_size (fixed) |
| Speed | Fast (minutes) | Slow (hours to days) |
| Accuracy | Statistical estimate | 100% exact |
| Use | Routine monitoring | Exhaustive check |
| Load | Low | High (controllable) |

## Possible Future Improvements

1. **Checksum pre-comparison**: compare a CRC32 per chunk, then compare rows only for mismatching chunks
2. **Adaptive delay**: adjust the delay automatically based on DB load
3. **Progress persistence**: save state so an interrupted run can resume

---

_Document created: 2026-01-26_
_Status: Implemented_
