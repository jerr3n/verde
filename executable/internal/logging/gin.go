// SPDX-License-Identifier: MPL-2.0

package logging

import (
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Middleware writes one log entry for each request.
// The level is Error for a 5xx status, Warn for a 4xx status, and Info for all other statuses.
func Middleware(l *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		query := c.Request.URL.RawQuery

		c.Next()

		status := c.Writer.Status()
		size := c.Writer.Size()
		// Size is -1 when no body is written yet.
		// For example, Gin writes the 404 body after the middleware runs.
		if size < 0 {
			size = 0
		}
		fields := []zap.Field{
			zap.Int("status", status),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.String("query", query),
			zap.String("route", c.FullPath()),
			zap.String("ip", c.ClientIP()),
			zap.String("user_agent", c.Request.UserAgent()),
			zap.Duration("latency", time.Since(start)),
			zap.Int("size", size),
		}
		if errs := c.Errors.ByType(gin.ErrorTypePrivate); len(errs) > 0 {
			fields = append(fields, zap.String("errors", errs.String()))
		}

		level := zapcore.InfoLevel
		switch {
		case status >= http.StatusInternalServerError:
			level = zapcore.ErrorLevel
		case status >= http.StatusBadRequest:
			level = zapcore.WarnLevel
		}

		l.Log(level, "request", fields...)
	}
}

// Recovery stops a panic in a handler, logs it with a stack trace, and sends status 500.
// If the panic comes from a broken client connection, Recovery logs it without a stack trace.
// It sends no response in that case, because the client cannot receive it.
func Recovery(l *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			if err, ok := rec.(error); ok && isBrokenPipe(err) {
				l.Error("connection broken",
					zap.Error(err),
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
				)
				_ = c.Error(err)
				c.Abort()
				return
			}

			l.Error("panic recovered",
				zap.Any("error", rec),
				zap.String("method", c.Request.Method),
				zap.String("path", c.Request.URL.Path),
				zap.Stack("stack"),
			)
			c.AbortWithStatus(http.StatusInternalServerError)
		}()

		c.Next()
	}
}

// RouteLogger returns a function for gin.DebugPrintRouteFunc.
// The function sends each route that Gin registers to zap at the Debug level.
// Gin calls it only in debug mode.
func RouteLogger(l *zap.Logger) func(httpMethod, absolutePath, handlerName string, nuHandlers int) {
	return func(httpMethod, absolutePath, handlerName string, nuHandlers int) {
		l.Debug("route registered",
			zap.String("method", httpMethod),
			zap.String("path", absolutePath),
			zap.String("handler", handlerName),
			zap.Int("handlers", nuHandlers),
		)
	}
}

// isBrokenPipe reports whether err comes from a client that closed or reset its connection.
func isBrokenPipe(err error) bool {
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}
	var sysErr *os.SyscallError
	if !errors.As(opErr, &sysErr) {
		return false
	}
	msg := strings.ToLower(sysErr.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
