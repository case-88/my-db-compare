# Retry Mode Specification

**Status**: Implemented (`internal/runner/retry.go`)

This document records the agreed design for running retry as a standalone
mode, separate from sample/full.

## Goals

- Provide a retry workflow for manually re-checking mismatch logs
- Fully separate retry from the sample/full comparison flow
- Reuse the existing output settings and log structure

## Run Modes

The program supports only these three modes:

- `sample`
- `full`
- `retry`

No other modes are assumed to exist. The run mode must always be recorded in the summary log.

## Configuration Policy

- Retry-specific settings are removed from the configuration.
- sample/full settings stay the same as before.
- Settings can be provided both via the config file and via command-line options.
- Retry reuses the existing output settings (`output.path`, `output.format`).

## Retry Mode Input

- The user specifies the path of a directory containing mismatch logs.
- The input directory is recorded in the summary **exactly as the user entered it**.
- If the directory contains no mismatch logs, the program exits with an error.

### Recognized Formats

Retry must recognize all three mismatch formats:

- `jsonl` (one `Mismatch` per line)
- `json` (array of `Mismatch`)
- `text` (parses `Query:` lines)

## Retry Re-verification

- Re-run each mismatch record's `query` against both the source and target DBs.
- Compare the results to check whether the mismatch still exists.
- If the mismatch persists, record it using the existing mismatch log rules:
  - `mismatch-*.{jsonl|json|txt}`

## Parallel Execution

- Retry mode uses `comparison.parallelism` to **run in parallel per table**.
- Multiple mismatch files belonging to the same table are **processed sequentially**.

## Output and Summary

Retry output follows the same directory structure as sample/full:

- Create the output directory according to the `output.path` rules
- Create `summary.json` in the same location

### Additional Summary Fields

Add the following fields to `summary.json`:

- `run_mode`: one of `sample|full|retry`
- `input_dir`: retry only (the user's raw input)

Mismatch records are not changed (no `run_mode` is added to records).

## Documentation Updates (Implementation Phase)

When implementing, reflect the changes in the following documents:

- `README.md`
- `config.yaml`
