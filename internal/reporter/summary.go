package reporter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Summary generates and displays comparison summaries
type Summary struct {
	mu              sync.RWMutex
	StartTime       time.Time
	EndTime         time.Time
	RunMode         string
	InputDir        string
	Config          *RunConfig
	Results         []*TableResult
	TotalTables     int
	TablesCompared  int
	TablesSkipped   int
	TablesFailed    int
	Error           string
	TotalSampled    int
	TotalMatched    int
	TotalMismatched int
	TotalVerified   int
	TotalResolved   int
}

// RunConfig holds the configuration used for this run
type RunConfig struct {
	SourceHost          string   `json:"source_host"`
	SourcePort          int      `json:"source_port"`
	TargetHost          string   `json:"target_host"`
	TargetPort          int      `json:"target_port"`
	SampleSize          int      `json:"sample_size"`
	Parallelism         int      `json:"parallelism"`
	IncludePatterns     []string `json:"include_patterns,omitempty"`
	ExcludePatterns     []string `json:"exclude_patterns,omitempty"`
	CheckTargetOnlyRows bool     `json:"check_target_only_rows"`
}

// TableResult holds the result for a single table
type TableResult struct {
	Table             string        `json:"table"`
	EstimatedRows     int64         `json:"estimated_rows"`
	KeyColumns        []string      `json:"key_columns"`
	Sampled           int           `json:"sampled"`
	Matched           int           `json:"matched"`
	MissingInTarget   int           `json:"missing_in_target"`
	MissingInSource   int           `json:"missing_in_source"`
	Different         int           `json:"different"`
	Verified          int           `json:"verified"`
	Resolved          int           `json:"resolved"`
	Duration          time.Duration `json:"duration"`
	Error             string        `json:"error,omitempty"`
	Skipped           bool          `json:"skipped"`
	SkipReason        string        `json:"skip_reason,omitempty"`
	TargetRowsChecked int           `json:"target_rows_checked"`
	TargetOnlyChecked bool          `json:"target_only_checked"`
}

// NewSummary creates a new Summary
func NewSummary() *Summary {
	return &Summary{
		StartTime: time.Now(),
		Results:   make([]*TableResult, 0),
	}
}

// AddResult adds a table result to the summary
func (s *Summary) AddResult(result *TableResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Results = append(s.Results, result)

	if result.Skipped {
		s.TablesSkipped++
	} else {
		if result.Error != "" {
			s.TablesFailed++
		} else {
			s.TablesCompared++
		}
		s.TotalSampled += result.Sampled
		s.TotalMatched += result.Matched
		s.TotalMismatched += result.MissingInTarget + result.MissingInSource + result.Different
		s.TotalVerified += result.Verified
		s.TotalResolved += result.Resolved
	}

	s.TotalTables++
}

// GetProgress returns current progress stats (thread-safe)
func (s *Summary) GetProgress() (completed, sampled, matched, verified int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalTables, s.TotalSampled, s.TotalMatched, s.TotalVerified
}

// Finish marks the summary as complete
func (s *Summary) Finish() {
	s.EndTime = time.Now()
}

// Duration returns the total duration
func (s *Summary) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}

// MatchRate returns the overall match rate as a percentage
func (s *Summary) MatchRate() float64 {
	if s.TotalSampled == 0 {
		return 0
	}
	return float64(s.TotalMatched) / float64(s.TotalSampled) * 100
}

// HasMismatches returns true if any mismatches were found
func (s *Summary) HasMismatches() bool {
	return s.TotalVerified > 0
}

func (s *Summary) HasErrors() bool {
	return s.Error != "" || s.TablesFailed > 0
}

// Incomplete는 미검증 테이블이 있는 실행을 일치 판정과 구분한다.
func (s *Summary) Incomplete() bool {
	if s.HasErrors() || s.TablesSkipped > 0 {
		return true
	}
	if s.RunMode != "retry" && s.Config != nil && s.Config.CheckTargetOnlyRows {
		for _, result := range s.Results {
			if !result.TargetOnlyChecked {
				return true
			}
		}
	}
	return false
}

func (s *Summary) Status() string {
	switch {
	case s.Incomplete():
		return "INCOMPLETE"
	case s.EndTime.IsZero():
		return "RUNNING"
	case s.HasMismatches():
		return "MISMATCHES FOUND"
	case s.RunMode == "retry":
		return "RETRY ROWS MATCHED"
	case s.RunMode == "sample":
		return "SAMPLED ROWS MATCHED"
	case s.Config != nil && s.Config.CheckTargetOnlyRows:
		return "ALL MATCHED"
	default:
		return "SOURCE ROWS MATCHED"
	}
}

// Print prints the summary to stdout
func (s *Summary) Print() {
	s.Fprint(os.Stdout)
}

// Fprint prints the summary to a writer
func (s *Summary) Fprint(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "════════════════════════════════════════════════════════════════")
	fmt.Fprintln(w, "                    COMPARISON SUMMARY                          ")
	fmt.Fprintln(w, "════════════════════════════════════════════════════════════════")
	fmt.Fprintln(w)

	// Overview
	fmt.Fprintf(w, "  Start Time:     %s\n", s.StartTime.Format(time.RFC3339))
	fmt.Fprintf(w, "  End Time:       %s\n", s.EndTime.Format(time.RFC3339))
	fmt.Fprintf(w, "  Duration:       %s\n", s.Duration().Round(time.Millisecond))
	fmt.Fprintln(w)

	// Table stats
	fmt.Fprintln(w, "  Tables:")
	fmt.Fprintf(w, "    Total:        %d\n", s.TotalTables)
	fmt.Fprintf(w, "    Compared:     %d\n", s.TablesCompared)
	fmt.Fprintf(w, "    Skipped:      %d\n", s.TablesSkipped)
	fmt.Fprintf(w, "    Failed:       %d\n", s.TablesFailed)
	fmt.Fprintln(w)

	// Comparison stats
	fmt.Fprintln(w, "  Comparison:")
	fmt.Fprintf(w, "    Sampled:      %d rows\n", s.TotalSampled)
	fmt.Fprintf(w, "    Matched:      %d rows (%.2f%%)\n", s.TotalMatched, s.MatchRate())
	fmt.Fprintf(w, "    Mismatched:   %d rows (initial)\n", s.TotalMismatched)
	fmt.Fprintln(w)

	// Verification stats
	fmt.Fprintln(w, "  Re-verification:")
	fmt.Fprintf(w, "    Verified:     %d rows (final mismatches)\n", s.TotalVerified)
	fmt.Fprintf(w, "    Resolved:     %d rows (replication lag)\n", s.TotalResolved)
	fmt.Fprintln(w)

	// Status
	fmt.Fprintf(w, "  Status:         %s\n", s.Status())
	if s.RunMode != "retry" {
		if s.Config == nil || !s.Config.CheckTargetOnlyRows {
			fmt.Fprintln(w, "  Target-only rows: NOT CHECKED")
		} else if s.RunMode == "sample" {
			fmt.Fprintln(w, "  Target-only rows: sampled keys only (see per-table completion)")
		} else {
			fmt.Fprintln(w, "  Target-only rows: full key scan (see per-table completion)")
		}
	}
	if s.Error != "" {
		fmt.Fprintf(w, "  Error:          %s\n", s.Error)
	}
	for _, result := range s.Results {
		if result.Error != "" {
			fmt.Fprintf(w, "  Unverified:     %s: %s\n", result.Table, result.Error)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "════════════════════════════════════════════════════════════════")

	// Per-table results if there are mismatches
	if s.HasMismatches() {
		s.printTableDetails(w)
	}
}

// printTableDetails prints detailed per-table results
func (s *Summary) printTableDetails(w io.Writer) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Tables with Mismatches:")
	fmt.Fprintln(w, "  ─────────────────────────────────────────────────────────────")

	for _, r := range s.Results {
		if r.Skipped || r.Verified == 0 {
			continue
		}

		fmt.Fprintf(w, "\n  %s:\n", r.Table)
		fmt.Fprintf(w, "    Sampled: %d, Matched: %d, Verified Mismatches: %d\n",
			r.Sampled, r.Matched, r.Verified)
		if r.MissingInTarget > 0 {
			fmt.Fprintf(w, "    - Missing in Target: %d\n", r.MissingInTarget)
		}
		if r.MissingInSource > 0 {
			fmt.Fprintf(w, "    - Missing in Source: %d\n", r.MissingInSource)
		}
		if r.Different > 0 {
			fmt.Fprintf(w, "    - Data Different: %d\n", r.Different)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ─────────────────────────────────────────────────────────────")
}

// PrintSkipped prints skipped tables
func (s *Summary) PrintSkipped(w io.Writer) {
	if s.TablesSkipped == 0 {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Skipped Tables:")
	fmt.Fprintln(w, "  ─────────────────────────────────────────────────────────────")

	for _, r := range s.Results {
		if !r.Skipped {
			continue
		}
		fmt.Fprintf(w, "    %s: %s\n", r.Table, r.SkipReason)
	}

	fmt.Fprintln(w, "  ─────────────────────────────────────────────────────────────")
}

// SetConfig sets the run configuration
func (s *Summary) SetConfig(cfg *RunConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Config = cfg
}

// ToJSON converts summary to JSON
func (s *Summary) ToJSON() ([]byte, error) {
	data := struct {
		StartTime       time.Time      `json:"start_time"`
		EndTime         time.Time      `json:"end_time,omitempty"`
		Duration        string         `json:"duration,omitempty"`
		RunMode         string         `json:"run_mode"`
		InputDir        string         `json:"input_dir,omitempty"`
		Config          *RunConfig     `json:"config,omitempty"`
		TotalTables     int            `json:"total_tables"`
		TablesCompared  int            `json:"tables_compared"`
		TablesSkipped   int            `json:"tables_skipped"`
		TablesFailed    int            `json:"tables_failed"`
		Error           string         `json:"error,omitempty"`
		Status          string         `json:"status"`
		TotalSampled    int            `json:"total_sampled"`
		TotalMatched    int            `json:"total_matched"`
		TotalMismatched int            `json:"total_mismatched"`
		TotalVerified   int            `json:"total_verified"`
		TotalResolved   int            `json:"total_resolved"`
		MatchRate       float64        `json:"match_rate"`
		HasMismatches   bool           `json:"has_mismatches"`
		Results         []*TableResult `json:"results"`
	}{
		StartTime:       s.StartTime,
		RunMode:         s.RunMode,
		InputDir:        s.InputDir,
		Config:          s.Config,
		TotalTables:     s.TotalTables,
		TablesCompared:  s.TablesCompared,
		TablesSkipped:   s.TablesSkipped,
		TablesFailed:    s.TablesFailed,
		Error:           s.Error,
		Status:          s.Status(),
		TotalSampled:    s.TotalSampled,
		TotalMatched:    s.TotalMatched,
		TotalMismatched: s.TotalMismatched,
		TotalVerified:   s.TotalVerified,
		TotalResolved:   s.TotalResolved,
		MatchRate:       s.MatchRate(),
		HasMismatches:   s.HasMismatches(),
		Results:         s.Results,
	}

	// Only include end_time and duration if comparison has finished
	if !s.EndTime.IsZero() {
		data.EndTime = s.EndTime
		data.Duration = s.Duration().String()
	}

	return json.MarshalIndent(data, "", "  ")
}

// SaveJSON saves the summary to a JSON file
func (s *Summary) SaveJSON(outputPath string) error {
	data, err := s.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal summary: %w", err)
	}

	// Create output directory if needed
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// No timestamp in filename - parent directory has timestamp
	filename := filepath.Join(outputPath, "summary.json")

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write summary file: %w", err)
	}

	return nil
}

// GetMismatchedTables returns tables with mismatches, sorted by mismatch count
func (s *Summary) GetMismatchedTables() []*TableResult {
	var mismatched []*TableResult

	for _, r := range s.Results {
		if !r.Skipped && r.Verified > 0 {
			mismatched = append(mismatched, r)
		}
	}

	// Sort by verified mismatch count descending
	sort.Slice(mismatched, func(i, j int) bool {
		return mismatched[i].Verified > mismatched[j].Verified
	})

	return mismatched
}

// FormatDuration formats a duration in human-readable form
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Millisecond).String()
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// ProgressBar generates a simple progress bar
func ProgressBar(current, total int, width int) string {
	if total == 0 {
		return strings.Repeat("░", width)
	}

	filled := int(float64(current) / float64(total) * float64(width))
	if filled > width {
		filled = width
	}

	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
