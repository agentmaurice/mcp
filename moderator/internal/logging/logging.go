package logging

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New returns a configured zap.Logger using the provided log level.
func New(level string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.Encoding = "json"
	cfg.EncoderConfig.TimeKey = "timestamp"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// Write logs to stdout instead of stderr
	cfg.OutputPaths = []string{"stdout"}
	cfg.ErrorOutputPaths = []string{"stderr"}

	zapLevel, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	cfg.Level = zap.NewAtomicLevelAt(zapLevel)

	return cfg.Build()
}

func parseLevel(level string) (zapcore.Level, error) {
	if level == "" {
		return zapcore.InfoLevel, nil
	}
	lvl := zapcore.InfoLevel
	if err := lvl.Set(strings.ToLower(level)); err != nil {
		return zapcore.InfoLevel, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	return lvl, nil
}
