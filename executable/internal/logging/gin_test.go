// SPDX-License-Identifier: MPL-2.0

package logging

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func newTestEngine(t *testing.T) (*gin.Engine, *observer.ObservedLogs) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	core, logs := observer.New(zapcore.DebugLevel)
	l := zap.New(core)

	r := gin.New()
	r.Use(Middleware(l), Recovery(l))
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/fail", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })
	r.GET("/panic", func(c *gin.Context) { panic("boom") })
	return r, logs
}

func do(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func requestEntry(t *testing.T, logs *observer.ObservedLogs) observer.LoggedEntry {
	t.Helper()
	entries := logs.FilterMessage("request").All()
	if len(entries) != 1 {
		t.Fatalf("want 1 request entry, got %d", len(entries))
	}
	return entries[0]
}

func TestMiddlewareLevels(t *testing.T) {
	tests := []struct {
		path   string
		status int
		level  zapcore.Level
	}{
		{"/ok?x=1", http.StatusOK, zapcore.InfoLevel},
		{"/missing", http.StatusNotFound, zapcore.WarnLevel},
		{"/fail", http.StatusInternalServerError, zapcore.ErrorLevel},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r, logs := newTestEngine(t)
			w := do(r, tt.path)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}

			e := requestEntry(t, logs)
			if e.Level != tt.level {
				t.Errorf("level = %v, want %v", e.Level, tt.level)
			}
			fields := e.ContextMap()
			if got := fields["status"]; got != int64(tt.status) {
				t.Errorf("status field = %v, want %d", got, tt.status)
			}
			if got := fields["method"]; got != http.MethodGet {
				t.Errorf("method field = %v", got)
			}
		})
	}
}

func TestMiddlewareFields(t *testing.T) {
	r, logs := newTestEngine(t)
	do(r, "/ok?x=1")

	fields := requestEntry(t, logs).ContextMap()
	want := map[string]any{"path": "/ok", "query": "x=1", "route": "/ok", "size": int64(2)}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %v, want %v", k, fields[k], v)
		}
	}
	for _, k := range []string{"ip", "user_agent", "latency"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
}

func TestRecovery(t *testing.T) {
	r, logs := newTestEngine(t)
	w := do(r, "/panic")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	panics := logs.FilterMessage("panic recovered").All()
	if len(panics) != 1 {
		t.Fatalf("want 1 panic entry, got %d", len(panics))
	}
	fields := panics[0].ContextMap()
	if fields["error"] != "boom" {
		t.Errorf("error = %v, want boom", fields["error"])
	}
	if s, _ := fields["stack"].(string); s == "" {
		t.Error("missing stack")
	}

	if e := requestEntry(t, logs); e.Level != zapcore.ErrorLevel {
		t.Errorf("request level = %v, want error", e.Level)
	}
}
