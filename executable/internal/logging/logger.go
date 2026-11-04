// Package logging provides a zap logging and Gin middleware that log through it.
package logging

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds a zap logging. In debug mode it writes coloured, human-readable
// console output at Debug level; otherwise it writes JSON at Info level.
//
// Automatic stack traces are off: they would only point at the middleware.
// Recovery adds a real stack trace to panic logs itself.
func New(debug bool) (*zap.Logger, error) {
	if debug {
		cfg := zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.DisableStacktrace = true
		return cfg.Build()
	}

	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.TimeKey = "time"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.DisableStacktrace = true
	return cfg.Build()
}
