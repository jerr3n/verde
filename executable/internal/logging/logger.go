// SPDX-License-Identifier: MPL-2.0

package logging

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New makes the logger for the server.
// In debug mode, the logger writes colored console text at the Debug level.
// In other modes, the logger writes JSON at the Info level.
//
// Automatic stack traces are off because they always point at the logging middleware.
// Recovery adds the correct stack trace to each panic log.
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
