package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config represents logging configuration
type Config struct {
	Level      string
	EnableFile bool
	LogDir     string
}

// New creates a new logger with the specified configuration
func New(level string) (*zap.Logger, error) {
	return NewWithConfig(Config{Level: level})
}

// NewWithConfig creates a new logger with full configuration
func NewWithConfig(cfg Config) (*zap.Logger, error) {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(cfg.Level)); err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create console encoder for stdout
	consoleEncoder := zapcore.NewJSONEncoder(encoderConfig)

	// Console output core
	consoleCore := zapcore.NewCore(
		consoleEncoder,
		zapcore.AddSync(os.Stdout),
		zapLevel,
	)

	cores := []zapcore.Core{consoleCore}

	// Add file output if enabled
	if cfg.EnableFile {
		logDir := cfg.LogDir
		if logDir == "" {
			logDir = "./logs"
		}

		// Ensure log directory exists, fallback to $TMPDIR/logs for
		// read-only filesystems (e.g. Docker containers with --read-only).
		if err := os.MkdirAll(logDir, 0755); err != nil {
			logDir = filepath.Join(os.TempDir(), "logs")
			if err2 := os.MkdirAll(logDir, 0755); err2 != nil {
				return nil, fmt.Errorf("failed to create log directory: %w (fallback: %w)", err, err2)
			}
		}

		// Create log file with timestamp for identification
		// Using a fixed name so it's easy to find, but truncate on each start
		logFile := filepath.Join(logDir, "rag-server.log")

		// Truncate file on start (create new or overwrite existing)
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to create log file: %w", err)
		}

		// Write header with startup info
		header := fmt.Sprintf("=== RAG Server Log - Started at %s ===\n\n",
			time.Now().Format(time.RFC3339))
		file.WriteString(header)

		// Create file encoder (more verbose for debugging)
		fileEncoderConfig := encoderConfig
		fileEncoderConfig.CallerKey = "caller"
		fileEncoderConfig.FunctionKey = "function"
		fileEncoder := zapcore.NewJSONEncoder(fileEncoderConfig)

		// File output core - always use debug level for file
		fileCore := zapcore.NewCore(
			fileEncoder,
			zapcore.AddSync(file),
			zapcore.DebugLevel,
		)

		cores = append(cores, fileCore)

		// Also create a human-readable log file for easier analysis
		readableLogFile := filepath.Join(logDir, "rag-server-readable.log")
		readableFile, err := os.OpenFile(readableLogFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to create readable log file: %w", err)
		}

		readableFile.WriteString(header)

		// Console-style encoder for human-readable logs
		readableEncoderConfig := zapcore.EncoderConfig{
			TimeKey:        "T",
			LevelKey:       "L",
			NameKey:        "N",
			CallerKey:      "C",
			FunctionKey:    zapcore.OmitKey,
			MessageKey:     "M",
			StacktraceKey:  "S",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalColorLevelEncoder,
			EncodeTime:     zapcore.TimeEncoderOfLayout("15:04:05.000"),
			EncodeDuration: zapcore.StringDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		}
		readableEncoder := zapcore.NewConsoleEncoder(readableEncoderConfig)

		readableCore := zapcore.NewCore(
			readableEncoder,
			zapcore.AddSync(readableFile),
			zapcore.DebugLevel,
		)

		cores = append(cores, readableCore)
	}

	// Combine all cores
	core := zapcore.NewTee(cores...)

	// Build logger with caller info
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return logger, nil
}
