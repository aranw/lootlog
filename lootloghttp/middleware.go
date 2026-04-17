// Package lootloghttp provides HTTP middleware for wide event logging.
package lootloghttp

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aranw/lootlog"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

// WriteHeader captures the status code and delegates to the underlying writer.
// Only the first call takes effect.
func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.statusCode = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

// Write delegates to the underlying ResponseWriter. The first call to Write
// implicitly commits a 200 status, so we mark the header as written to prevent
// a later WriteHeader call from recording the wrong status code.
func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.wroteHeader = true
	return rw.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter. This allows
// http.ResponseController to access optional interfaces (http.Flusher,
// http.Hijacker, etc.) on the original writer.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Middleware returns an HTTP middleware that creates a WideEventLogger for each
// request, attaches it to the request context, and emits a wide event log line
// when the request completes.
//
// The middleware is compatible with any router that uses the standard
// func(http.Handler) http.Handler middleware pattern.
func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wel := lootlog.New(logger)
			wel.AddString("method", r.Method)
			wel.AddString("path", r.URL.Path)

			ctx := lootlog.WithContext(r.Context(), wel)
			rw := wrapResponseWriter(w)

			defer func() {
				var panicVal any
				if v := recover(); v != nil {
					panicVal = v
					wel.AddString("panic", fmt.Sprint(v))
					rw.statusCode = http.StatusInternalServerError
				}

				status := rw.statusCode
				wel.AddInt("status_code", status)

				var outcome string
				var level slog.Level
				switch {
				case status >= 500:
					outcome = "server_error"
					level = slog.LevelError
				case status >= 400:
					outcome = "client_error"
					level = slog.LevelWarn
				default:
					outcome = "success"
					level = slog.LevelInfo
				}

				wel.AddString("outcome", outcome)
				wel.Emit(ctx, level, "http request")

				if panicVal != nil {
					panic(panicVal)
				}
			}()

			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}
