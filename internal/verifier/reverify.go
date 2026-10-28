package verifier

import (
	"context"
	"time"

	"github.com/case-88/my-db-compare/internal/comparator"
	"github.com/case-88/my-db-compare/internal/sampler"
	"github.com/case-88/my-db-compare/pkg/types"
	"go.uber.org/zap"
)

// Verifier re-verifies mismatches to account for replication lag
type Verifier struct {
	srcSampler    *sampler.Sampler
	targetSampler *sampler.Sampler
	logger        *zap.Logger
	retryDelay    time.Duration
	compOptions   comparator.ComparatorOptions
}

// NewVerifier creates a new Verifier
func NewVerifier(srcSampler, targetSampler *sampler.Sampler, retryDelay time.Duration, logger *zap.Logger) *Verifier {
	return NewVerifierWithOptions(srcSampler, targetSampler, retryDelay, logger, comparator.DefaultComparatorOptions())
}

// NewVerifierWithOptions creates a new Verifier with comparator options
func NewVerifierWithOptions(srcSampler, targetSampler *sampler.Sampler, retryDelay time.Duration, logger *zap.Logger, compOptions comparator.ComparatorOptions) *Verifier {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Verifier{
		srcSampler:    srcSampler,
		targetSampler: targetSampler,
		logger:        logger,
		retryDelay:    retryDelay,
		compOptions:   compOptions,
	}
}

// VerifyResult holds the result of re-verification
type VerifyResult struct {
	OriginalCount  int              // Original mismatch count
	VerifiedCount  int              // Count after re-verification (true mismatches)
	ResolvedCount  int              // Mismatches that resolved (replication lag)
	Mismatches     []types.Mismatch // Verified mismatches
	Duration       time.Duration    // Time spent on verification
}

// Verify re-verifies a list of mismatches
// Returns only the mismatches that still exist after re-verification
func (v *Verifier) Verify(ctx context.Context, table types.TableMeta, mismatches []types.Mismatch) (*VerifyResult, error) {
	start := time.Now()

	if len(mismatches) == 0 {
		return &VerifyResult{
			Duration: time.Since(start),
		}, nil
	}

	v.logger.Info("starting re-verification",
		zap.String("table", table.FullName()),
		zap.Int("mismatches", len(mismatches)),
		zap.Duration("delay", v.retryDelay),
	)

	// Wait for replication to catch up
	if v.retryDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(v.retryDelay):
		}
	}

	comp := comparator.NewComparatorWithOptions(table.KeyColumns(), v.compOptions)
	var verified []types.Mismatch

	for _, mismatch := range mismatches {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		stillMismatch, newMismatch, err := v.reverifyOne(ctx, table, mismatch, comp)
		if err != nil {
			v.logger.Warn("re-verification failed for row",
				zap.String("table", table.FullName()),
				zap.Any("pk", mismatch.PK),
				zap.Error(err),
			)
			// Keep original mismatch if re-verification fails
			verified = append(verified, mismatch)
			continue
		}

		if stillMismatch {
			verified = append(verified, *newMismatch)
		}
	}

	result := &VerifyResult{
		OriginalCount: len(mismatches),
		VerifiedCount: len(verified),
		ResolvedCount: len(mismatches) - len(verified),
		Mismatches:    verified,
		Duration:      time.Since(start),
	}

	v.logger.Info("re-verification completed",
		zap.String("table", table.FullName()),
		zap.Int("original", result.OriginalCount),
		zap.Int("verified", result.VerifiedCount),
		zap.Int("resolved", result.ResolvedCount),
		zap.Duration("duration", result.Duration),
	)

	return result, nil
}

// reverifyOne re-verifies a single mismatch
// Returns (stillMismatch, newMismatch, error)
func (v *Verifier) reverifyOne(ctx context.Context, table types.TableMeta, mismatch types.Mismatch, comp *comparator.Comparator) (bool, *types.Mismatch, error) {
	// Fetch current data from both source and target
	srcRow, err := v.srcSampler.SampleByKey(ctx, table, mismatch.PK)
	if err != nil {
		return false, nil, err
	}

	targetRow, err := v.targetSampler.SampleByKey(ctx, table, mismatch.PK)
	if err != nil {
		return false, nil, err
	}

	now := time.Now()

	// Case 1: Row missing in both (deleted from source, replicated)
	if srcRow == nil && targetRow == nil {
		return false, nil, nil // Resolved
	}

	// Build lookup query for this row
	query := types.BuildLookupQuery(mismatch.Table, mismatch.PK)

	// Case 2: Row exists only in source (still missing in target)
	if srcRow != nil && targetRow == nil {
		return true, &types.Mismatch{
			Table:      mismatch.Table,
			PK:         mismatch.PK,
			Type:       types.MismatchMissingTarget,
			SourceData: srcRow,
			Query:      query,
			Timestamp:  now,
		}, nil
	}

	// Case 3: Row exists only in target (deleted from source)
	if srcRow == nil && targetRow != nil {
		return true, &types.Mismatch{
			Table:      mismatch.Table,
			PK:         mismatch.PK,
			Type:       types.MismatchMissingSource,
			TargetData: targetRow,
			Query:      query,
			Timestamp:  now,
		}, nil
	}

	// Case 4: Row exists in both - compare data
	diffCols := comp.CompareRows(srcRow, targetRow)
	if len(diffCols) == 0 {
		return false, nil, nil // Data now matches - resolved
	}

	// Still different
	return true, &types.Mismatch{
		Table:       mismatch.Table,
		PK:          mismatch.PK,
		Type:        types.MismatchDataDiff,
		DiffColumns: diffCols,
		SourceData:  srcRow,
		TargetData:  targetRow,
		Query:       query,
		Timestamp:   now,
	}, nil
}

