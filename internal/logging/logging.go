// Package logging configures a structured zap logger for the process and a
// request-scoped logger stored on the context.
package logging

import (
	"context"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ctxKey struct{}

// New constructs a zap.Logger at the given level. Production uses JSON to stdout.
func New(level string, development bool) (*zap.Logger, error) {
	var lvl zapcore.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, err
	}
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	cfg.Encoding = "json"
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	if development {
		cfg.Encoding = "console"
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}
	cfg.DisableStacktrace = false
	cfg.InitialFields = map[string]any{"svc": "sitesync"}
	return cfg.Build(zap.AddCallerSkip(0))
}

// IntoContext returns a child context carrying the logger.
func IntoContext(ctx context.Context, logger *zap.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext returns the logger on ctx, falling back to a no-op logger so
// callers never need to nil-check.
func FromContext(ctx context.Context) *zap.Logger {
	if v, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok && v != nil {
		return v
	}
	return zap.NewNop()
}
