package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/case-88/my-db-compare/internal/config"
	"github.com/case-88/my-db-compare/internal/runner"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

const (
	runModeSample = "sample"
	runModeFull   = "full"
	runModeRetry  = "retry"
)

type cliOptions struct {
	runMode            string
	configFile         string
	outputDir          string
	logLevel           string
	showVersion        bool
	dryRun             bool
	tables             string
	sampleSize         int
	parallelism        int
	timeout            time.Duration
	chunkSize          int
	delayMs            int
	ignoreDatetime     string
	ignoreZero         string
	ignoreJSON         string
	ignoreLarge        string
	generateCorrection string
	inputDir           string
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	if isVersionFlag(os.Args[1]) {
		fmt.Printf("db-compare version %s (built %s)\n", version, buildTime)
		os.Exit(0)
	}
	if isHelpFlag(os.Args[1]) {
		printUsage()
		os.Exit(0)
	}

	opts, err := parseArgs(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printUsage()
		os.Exit(2)
	}

	if opts.showVersion {
		fmt.Printf("db-compare version %s (built %s)\n", version, buildTime)
		os.Exit(0)
	}

	// Load configuration first (for log level)
	cfg, err := config.LoadConfig(opts.configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Determine log level: CLI > config > default
	effectiveLogLevel := opts.logLevel
	if effectiveLogLevel == "info" && cfg.Output.LogLevel != "" {
		effectiveLogLevel = cfg.Output.LogLevel
	}

	// Setup logger
	logger, err := setupLogger(effectiveLogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to setup logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("start",
		zap.String("version", version),
		zap.String("config", opts.configFile),
		zap.String("mode", opts.runMode),
	)

	// Apply command line overrides
	applyOverrides(cfg, opts.tables, opts.sampleSize, opts.parallelism, opts.chunkSize, opts.delayMs,
		opts.ignoreDatetime, opts.ignoreZero, opts.ignoreJSON, opts.ignoreLarge, opts.generateCorrection)
	if opts.runMode == runModeFull {
		cfg.Comparison.Mode = "full"
	} else if opts.runMode == runModeSample {
		cfg.Comparison.Mode = "sampling"
	}

	// Use config output path if not overridden by command line
	baseOutputDir := opts.outputDir
	if opts.outputDir == "./output" && cfg.Output.Path != "" {
		baseOutputDir = cfg.Output.Path
	}

	// Create timestamp subdirectory under output path
	timestamp := time.Now().Format("2006-01-02T15:04:05")
	actualOutputDir := filepath.Join(baseOutputDir, timestamp)

	// Create output directory immediately
	if err := os.MkdirAll(actualOutputDir, 0755); err != nil {
		logger.Fatal("failed to create output directory", zap.String("path", actualOutputDir), zap.Error(err))
	}

	logger.Info("output ready",
		zap.String("output", actualOutputDir),
	)

	// Validate configuration
	if err := cfg.ValidateForMode(opts.runMode); err != nil {
		logger.Fatal("invalid configuration", zap.Error(err))
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Apply timeout if specified
	if opts.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("shutdown", zap.String("signal", sig.String()))
		cancel()
	}()

	// Create and run the comparison
	r, err := runner.New(cfg, actualOutputDir, opts.runMode, opts.inputDir, logger)
	if err != nil {
		logger.Fatal("failed to create runner", zap.Error(err))
	}

	// Dry run mode
	if opts.dryRun {
		if opts.runMode == runModeRetry {
			logger.Fatal("dry-run is not supported in retry mode")
		}
		if err := r.DryRun(ctx); err != nil {
			logger.Fatal("dry run failed", zap.Error(err))
		}
		os.Exit(0)
	}

	// Run comparison
	result, err := r.Run(ctx)
	if err != nil {
		if ctx.Err() == context.Canceled {
			logger.Warn("comparison was canceled")
			os.Exit(130) // 128 + SIGINT
		}
		if ctx.Err() == context.DeadlineExceeded {
			logger.Warn("comparison timed out")
			os.Exit(124) // timeout exit code
		}
		logger.Fatal("comparison failed", zap.Error(err))
	}

	// Print summary
	result.Summary.Print()

	// Exit with appropriate code
	if result.Summary.HasMismatches() {
		logger.Info("comparison completed with mismatches",
			zap.Int("mismatches", result.Summary.TotalVerified),
		)
		os.Exit(1)
	}

	logger.Info("comparison completed successfully",
		zap.Duration("duration", result.Summary.Duration()),
	)
	os.Exit(0)
}

func setupLogger(level string) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	switch level {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	config := zap.Config{
		Level:             zap.NewAtomicLevelAt(zapLevel),
		Development:       false,
		DisableCaller:     true,
		DisableStacktrace: true,
		Encoding:          "console",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:          "ts",
			LevelKey:         "level",
			NameKey:          "logger",
			MessageKey:       "msg",
			StacktraceKey:    "stacktrace",
			LineEnding:       zapcore.DefaultLineEnding,
			ConsoleSeparator: " ",
			EncodeLevel:      zapcore.CapitalLevelEncoder,
			EncodeTime:       zapcore.TimeEncoderOfLayout("15:04:05.000"),
			EncodeDuration:   zapcore.StringDurationEncoder,
		},
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}

	return config.Build()
}

func applyOverrides(cfg *config.Config, tables string, sampleSize, parallelism int, chunkSize, delayMs int,
	ignoreDatetime, ignoreZero, ignoreJSON, ignoreLarge, generateCorrection string) {
	if tables != "" {
		cfg.Tables.Include = splitTables(tables)
	}
	if sampleSize > 0 {
		cfg.Comparison.SampleSize = sampleSize
	}
	if parallelism > 0 {
		cfg.Comparison.Parallelism = parallelism
		cfg.Comparison.FullModeParallelism = parallelism // also applies to full mode
	}

	// Full mode options
	if chunkSize > 0 {
		cfg.Comparison.FullModeChunkSize = chunkSize
	}
	if delayMs >= 0 {
		cfg.Comparison.FullModeDelayMs = delayMs
	}

	// Ignore options
	if v := parseBool(ignoreDatetime); v != nil {
		cfg.Comparison.IgnoreDatetimePrecision = v
	}
	if v := parseBool(ignoreZero); v != nil {
		cfg.Comparison.IgnoreZeroDatetime = v
	}
	if v := parseBool(ignoreJSON); v != nil {
		cfg.Comparison.IgnoreJSONKeyOrder = v
	}
	if v := parseBool(ignoreLarge); v != nil {
		cfg.Comparison.IgnoreLargeColumns = v
	}
	if v := parseBool(generateCorrection); v != nil {
		cfg.Comparison.GenerateCorrectionQueries = v
	}
}

func parseBool(s string) *bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "true" || s == "1" || s == "yes" {
		v := true
		return &v
	}
	if s == "false" || s == "0" || s == "no" {
		v := false
		return &v
	}
	return nil
}

func splitTables(s string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if start < i {
				result = append(result, s[start:i])
			}
			start = i + 1
		}
	}
	return result
}

func parseArgs(args []string) (*cliOptions, error) {
	mode := args[1]
	if mode != runModeSample && mode != runModeFull && mode != runModeRetry {
		return nil, fmt.Errorf("invalid run mode: %s", mode)
	}

	opts := &cliOptions{runMode: mode}
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&opts.configFile, "config", "config.yaml", "Path to configuration file")
	fs.StringVar(&opts.outputDir, "output", "./output", "Directory for output files")
	fs.StringVar(&opts.logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	fs.BoolVar(&opts.showVersion, "version", false, "Show version information")
	fs.DurationVar(&opts.timeout, "timeout", 0, "Overall timeout (0 = no timeout)")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "Perform a dry run (show what would be compared)")

	fs.StringVar(&opts.ignoreDatetime, "ignore-datetime-precision", "", "Ignore datetime microseconds (true/false, default: true)")
	fs.StringVar(&opts.ignoreZero, "ignore-zero-datetime", "", "Treat 0000-00-00 as NULL (true/false, default: true)")
	fs.StringVar(&opts.ignoreJSON, "ignore-json-key-order", "", "Ignore JSON key order (true/false, default: true)")
	fs.StringVar(&opts.ignoreLarge, "ignore-large-columns", "", "Skip TEXT/BLOB/JSON columns (true/false, default: false)")
	fs.StringVar(&opts.generateCorrection, "generate-correction-queries", "", "Generate correction queries (true/false, default: false)")

	if mode == runModeRetry {
		fs.StringVar(&opts.inputDir, "input", "", "Directory containing mismatch logs to retry")
	} else {
		fs.StringVar(&opts.tables, "tables", "", "Override tables to compare (comma-separated, e.g., 'db1.users,db2.*')")
		fs.IntVar(&opts.sampleSize, "sample-size", 0, "Override sample size per table (0 = use config)")
		fs.IntVar(&opts.parallelism, "parallelism", 0, "Override parallelism (0 = use config)")
		fs.IntVar(&opts.chunkSize, "chunk-size", 0, "Full mode: chunk size per query (0 = use config, default 2000)")
		fs.IntVar(&opts.delayMs, "delay-ms", -1, "Full mode: delay between chunks in ms (-1 = use config, default 10)")
	}

	if err := fs.Parse(args[2:]); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	if mode == runModeRetry && strings.TrimSpace(opts.inputDir) == "" {
		return nil, fmt.Errorf("retry mode requires --input")
	}

	return opts, nil
}

func printUsage() {
	name := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, "Usage: %s <sample|full|retry> [flags]\n", name)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Modes:")
	fmt.Fprintln(os.Stderr, "  sample  Sampling comparison (default behavior)")
	fmt.Fprintln(os.Stderr, "  full    Full comparison")
	fmt.Fprintln(os.Stderr, "  retry   Re-verify mismatch logs")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Retry requires: --input <mismatch-logs-dir>")
}

func isVersionFlag(arg string) bool {
	return arg == "--version" || arg == "-version"
}

func isHelpFlag(arg string) bool {
	return arg == "--help" || arg == "-h"
}
