// Package lootlog provides wide event logging built on log/slog.
//
// Wide event logging is a structured logging pattern where a single log event
// accumulates attributes throughout an operation's lifecycle (e.g., an HTTP
// request) and emits them all at once at the end. Instead of many small log
// lines, you get one "wide" log line containing all collected context.
package lootlog

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type contextKey struct{}

// WideEventLogger collects attributes over the lifetime of an operation and
// emits them as a single wide log event.
type WideEventLogger struct {
	logger *slog.Logger
	start  time.Time

	mu    sync.Mutex
	attrs []slog.Attr
}

// New creates a new WideEventLogger that wraps the given slog.Logger.
// If logger is nil, slog.Default() is used. It records the current time
// as the start time and pre-allocates space for 16 attributes.
func New(logger *slog.Logger) *WideEventLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return &WideEventLogger{
		logger: logger,
		start:  time.Now(),
		attrs:  make([]slog.Attr, 0, 16),
	}
}

// WithContext stores the WideEventLogger in the given context.
func WithContext(ctx context.Context, wel *WideEventLogger) context.Context {
	return context.WithValue(ctx, contextKey{}, wel)
}

// FromContext retrieves the WideEventLogger from the context.
// Returns nil if no logger is present.
func FromContext(ctx context.Context) *WideEventLogger {
	wel, _ := ctx.Value(contextKey{}).(*WideEventLogger)
	return wel
}

// Add adds a generic attribute with the given key and slog.Value.
// Attributes are appended, so adding the same key more than once will produce
// duplicate keys in the emitted log event (consistent with slog semantics).
func (w *WideEventLogger) Add(key string, value slog.Value) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Attr{Key: key, Value: value})
	w.mu.Unlock()
}

// AddString adds a string attribute.
func (w *WideEventLogger) AddString(key, value string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.String(key, value))
	w.mu.Unlock()
}

// AddInt adds an int attribute.
func (w *WideEventLogger) AddInt(key string, value int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Int(key, value))
	w.mu.Unlock()
}

// AddInt64 adds an int64 attribute.
func (w *WideEventLogger) AddInt64(key string, value int64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Int64(key, value))
	w.mu.Unlock()
}

// AddBool adds a bool attribute.
func (w *WideEventLogger) AddBool(key string, value bool) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Bool(key, value))
	w.mu.Unlock()
}

// AddFloat64 adds a float64 attribute.
func (w *WideEventLogger) AddFloat64(key string, value float64) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Float64(key, value))
	w.mu.Unlock()
}

// AddDuration adds a time.Duration attribute.
func (w *WideEventLogger) AddDuration(key string, value time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Duration(key, value))
	w.mu.Unlock()
}

// AddTime adds a time.Time attribute.
func (w *WideEventLogger) AddTime(key string, value time.Time) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Time(key, value))
	w.mu.Unlock()
}

// AddAny adds an attribute using slog.AnyValue.
func (w *WideEventLogger) AddAny(key string, value any) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Any(key, value))
	w.mu.Unlock()
}

// AddObject adds a nested group of attributes under the given key.
func (w *WideEventLogger) AddObject(key string, attrs ...slog.Attr) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Attr{
		Key:   key,
		Value: slog.GroupValue(attrs...),
	})
	w.mu.Unlock()
}

// AddError adds a structured error under an "error" group key.
// The group contains "type" and "message" attributes, plus any extra
// attributes provided. If err is nil the call is a no-op.
func (w *WideEventLogger) AddError(err error, errType string, extraAttrs ...slog.Attr) {
	if w == nil || err == nil {
		return
	}
	groupAttrs := make([]slog.Attr, 0, 2+len(extraAttrs))
	groupAttrs = append(groupAttrs,
		slog.String("type", errType),
		slog.String("message", err.Error()),
	)
	groupAttrs = append(groupAttrs, extraAttrs...)

	w.mu.Lock()
	w.attrs = append(w.attrs, slog.Attr{
		Key:   "error",
		Value: slog.GroupValue(groupAttrs...),
	})
	w.mu.Unlock()
}

// Emit logs all accumulated attributes as a single wide event at the given
// level. It automatically appends a duration_ms attribute measuring the time
// elapsed since the logger was created. The context is forwarded to the
// underlying slog handler, making request-scoped values (e.g., trace IDs)
// available to handlers that inspect it.
//
// Emit may be called more than once. Each call emits a separate log event
// with all attributes accumulated so far and an independent duration_ms.
func (w *WideEventLogger) Emit(ctx context.Context, level slog.Level, msg string) {
	if w == nil {
		return
	}
	durationMs := float64(time.Since(w.start)) / float64(time.Millisecond)

	w.mu.Lock()
	attrs := make([]slog.Attr, len(w.attrs), len(w.attrs)+1)
	copy(attrs, w.attrs)
	w.mu.Unlock()

	attrs = append(attrs, slog.Float64("duration_ms", durationMs))
	w.logger.LogAttrs(ctx, level, msg, attrs...)
}
