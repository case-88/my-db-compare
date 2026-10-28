# Known Issues

## Partition Bias - Sample Count May Fall Short

### Problem

The current sampling method splits the PK range evenly into partitions and extracts `N/M` rows from each partition. However, **if the data distribution does not match the PK range**, the target sample count may not be reached.

### Example

Target: sample 10,000 rows (10 partitions × 1,000 rows)

```
PK Range: 1 ~ 100,000 (split into 10 partitions)

Partition 1: PK 1-10,000      → 8,000 actual rows → 1,000 extracted ✓
Partition 2: PK 10,001-20,000 → 500 actual rows   → only 500 extracted ✗
Partition 3: PK 20,001-30,000 → 0 actual rows     → 0 extracted ✗
Partition 4: PK 30,001-40,000 → 100 actual rows   → only 100 extracted ✗
...

Result: only about 6,000 of the 10,000 target rows are sampled
```

### Root Cause

1. **Deleted data**: the PK existed but the row was deleted (excluding soft deletes)
2. **Uneven inserts**: data is concentrated in specific PK ranges
3. **Auto-increment gaps**: PKs skipped due to rollbacks, failures, etc.
4. **Time-based PKs**: data concentrated in specific time periods

### Impact

- Target sample count not met (can drop by up to several tens of percent)
- Reduced comparison coverage
- Certain data ranges may be underrepresented in the sample

### Current Behavior

- Each partition is extracted with a LIMIT, so if a partition has too little data, it simply comes up short
- The shortfall is not made up from other partitions
- The final sample count may be lower than the target

### Potential Solutions (Not Implemented)

1. **Adaptive Sampling**: detect short partitions and take extra samples from other partitions
2. **Pre-count**: query the actual row count per partition first and adjust quotas
3. **Histogram-based**: split partitions using a histogram of the data distribution
4. **Over-sampling**: sample more than the target, then select randomly

### Workaround

- Set `sample_size` higher than the target (e.g. target 10,000 → set 15,000)
- Be careful when `information_schema.TABLE_ROWS` is inaccurate

### Affected Tables

The issue is more likely in tables with the following characteristics:

| Characteristic | Example |
|------|------|
| History of mass deletes | Log tables, temporary data |
| Time-based PK | DATETIME/TIMESTAMP PK |
| Uneven inserts | Rows created mostly during specific periods |
| Sharding/partitioning | Physical partitions don't match the logical PK range |
