package lootlog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// captureHandler is a test slog handler that captures log records.
type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	Level    slog.Level
	Message  string
	Attrs    map[string]slog.Value
	NumAttrs int
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{
		Level:    r.Level,
		Message:  r.Message,
		Attrs:    make(map[string]slog.Value),
		NumAttrs: r.NumAttrs(),
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler       { return h }

func (h *captureHandler) last() capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.records[len(h.records)-1]
}

func (h *captureHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func TestNewWithNilLogger(t *testing.T) {
	wel := New(nil)
	wel.AddString("key", "val")
	// Should not panic — falls back to slog.Default().
	wel.Emit(context.Background(), slog.LevelInfo, "nil logger test")
}

func TestBasicAttributeAccumulation(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	wel.AddString("method", "GET")
	wel.AddString("path", "/api/users")
	wel.AddInt("status_code", 200)
	wel.AddInt64("bytes", 1024)
	wel.AddBool("cached", true)
	wel.AddFloat64("score", 99.5)
	wel.AddAny("tags", []string{"api", "v2"})
	wel.Add("custom", slog.StringValue("val"))
	wel.AddDuration("latency", 150*time.Millisecond)
	wel.AddTime("created_at", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	wel.Emit(context.Background(), slog.LevelInfo, "request completed")

	if h.count() != 1 {
		t.Fatalf("expected 1 record, got %d", h.count())
	}

	rec := h.last()
	if rec.Level != slog.LevelInfo {
		t.Errorf("expected level Info, got %v", rec.Level)
	}
	if rec.Message != "request completed" {
		t.Errorf("expected message 'request completed', got %q", rec.Message)
	}

	assertAttr(t, rec, "method", "GET")
	assertAttr(t, rec, "path", "/api/users")
	assertAttrInt(t, rec, "status_code", 200)

	if _, ok := rec.Attrs["duration_ms"]; !ok {
		t.Error("expected duration_ms attribute")
	}
	if _, ok := rec.Attrs["bytes"]; !ok {
		t.Error("expected bytes attribute")
	}
	if _, ok := rec.Attrs["cached"]; !ok {
		t.Error("expected cached attribute")
	}
	if _, ok := rec.Attrs["score"]; !ok {
		t.Error("expected score attribute")
	}
	if _, ok := rec.Attrs["tags"]; !ok {
		t.Error("expected tags attribute")
	}
	if _, ok := rec.Attrs["custom"]; !ok {
		t.Error("expected custom attribute")
	}
	if _, ok := rec.Attrs["latency"]; !ok {
		t.Error("expected latency attribute")
	}
	if _, ok := rec.Attrs["created_at"]; !ok {
		t.Error("expected created_at attribute")
	}
}

func TestNilSafety(t *testing.T) {
	var wel *WideEventLogger

	// None of these should panic.
	wel.Add("key", slog.StringValue("val"))
	wel.AddString("key", "val")
	wel.AddInt("key", 1)
	wel.AddInt64("key", 1)
	wel.AddBool("key", true)
	wel.AddFloat64("key", 1.0)
	wel.AddDuration("key", time.Second)
	wel.AddTime("key", time.Now())
	wel.AddAny("key", "val")
	wel.AddObject("key", slog.String("nested", "val"))
	wel.AddError(errors.New("err"), "test")
	wel.Emit(context.Background(), slog.LevelInfo, "msg")
}

func TestThreadSafety(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			wel.AddString(fmt.Sprintf("key_%d", n), fmt.Sprintf("val_%d", n))
		}(i)
	}
	wg.Wait()

	wel.Emit(context.Background(), slog.LevelInfo, "concurrent test")

	rec := h.last()
	// 100 goroutines * 1 attr each + duration_ms = 101
	if rec.NumAttrs != 101 {
		t.Errorf("expected 101 attributes, got %d", rec.NumAttrs)
	}
	for i := range 100 {
		key := fmt.Sprintf("key_%d", i)
		assertAttr(t, rec, key, fmt.Sprintf("val_%d", i))
	}
}

func TestContextRoundTrip(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	ctx := WithContext(context.Background(), wel)
	got := FromContext(ctx)

	if got != wel {
		t.Error("expected to get the same WideEventLogger from context")
	}
}

func TestFromContextReturnsNilForEmptyContext(t *testing.T) {
	got := FromContext(context.Background())
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestAddError(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	wel.AddError(
		errors.New("connection refused"),
		"database_error",
		slog.String("host", "db.example.com"),
	)

	wel.Emit(context.Background(), slog.LevelError, "operation failed")

	rec := h.last()
	errVal, ok := rec.Attrs["error"]
	if !ok {
		t.Fatal("expected error attribute")
	}

	if errVal.Kind() != slog.KindGroup {
		t.Fatalf("expected error to be a group, got %v", errVal.Kind())
	}

	groupAttrs := make(map[string]string)
	for _, a := range errVal.Group() {
		groupAttrs[a.Key] = a.Value.String()
	}

	if groupAttrs["type"] != "database_error" {
		t.Errorf("expected error type 'database_error', got %q", groupAttrs["type"])
	}
	if groupAttrs["message"] != "connection refused" {
		t.Errorf("expected error message 'connection refused', got %q", groupAttrs["message"])
	}
	if groupAttrs["host"] != "db.example.com" {
		t.Errorf("expected error host 'db.example.com', got %q", groupAttrs["host"])
	}
}

func TestAddObject(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	wel.AddObject("user",
		slog.String("id", "usr_123"),
		slog.String("role", "admin"),
	)

	wel.Emit(context.Background(), slog.LevelInfo, "test")

	rec := h.last()
	userVal, ok := rec.Attrs["user"]
	if !ok {
		t.Fatal("expected user attribute")
	}
	if userVal.Kind() != slog.KindGroup {
		t.Fatalf("expected user to be a group, got %v", userVal.Kind())
	}
}

func TestDurationIsPositive(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	wel.AddString("test", "duration")
	wel.Emit(context.Background(), slog.LevelInfo, "done")

	rec := h.last()
	durVal, ok := rec.Attrs["duration_ms"]
	if !ok {
		t.Fatal("expected duration_ms attribute")
	}
	dur := durVal.Float64()
	if dur < 0 {
		t.Errorf("expected positive duration, got %f", dur)
	}
}

func TestAddErrorNilError(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	// Should not panic and should not add an error attribute.
	wel.AddError(nil, "some_type")
	wel.Emit(context.Background(), slog.LevelInfo, "test")

	rec := h.last()
	if _, ok := rec.Attrs["error"]; ok {
		t.Error("expected no error attribute when err is nil")
	}
}

func TestEmitForwardsContext(t *testing.T) {
	type ctxKey struct{}

	var captured context.Context
	handler := &contextCaptureHandler{onHandle: func(ctx context.Context) {
		captured = ctx
	}}
	logger := slog.New(handler)
	wel := New(logger)

	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-123")
	wel.Emit(ctx, slog.LevelInfo, "test")

	if captured == nil {
		t.Fatal("expected handler to be called")
	}
	if captured.Value(ctxKey{}) != "trace-123" {
		t.Error("expected context value to be forwarded to slog handler")
	}
}

// contextCaptureHandler captures the context passed to Handle.
type contextCaptureHandler struct {
	onHandle func(context.Context)
}

func (h *contextCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *contextCaptureHandler) Handle(ctx context.Context, _ slog.Record) error {
	h.onHandle(ctx)
	return nil
}
func (h *contextCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *contextCaptureHandler) WithGroup(string) slog.Handler      { return h }

func TestPreAllocation(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	if cap(wel.attrs) != 16 {
		t.Errorf("expected pre-allocated capacity of 16, got %d", cap(wel.attrs))
	}
}

func TestAttrsGrowBeyondPreAllocation(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)
	wel := New(logger)

	// Add more attributes than the pre-allocated capacity of 16.
	for i := range 32 {
		wel.AddInt(fmt.Sprintf("attr_%d", i), i)
	}

	wel.Emit(context.Background(), slog.LevelInfo, "grow test")

	rec := h.last()
	// 32 explicitly added + duration_ms = 33
	if len(rec.Attrs) != 33 {
		t.Errorf("expected 33 attributes, got %d", len(rec.Attrs))
	}
	for i := range 32 {
		key := fmt.Sprintf("attr_%d", i)
		assertAttrInt(t, rec, key, int64(i))
	}
}

func assertAttr(t *testing.T, rec capturedRecord, key, expected string) {
	t.Helper()
	val, ok := rec.Attrs[key]
	if !ok {
		t.Errorf("expected attribute %q", key)
		return
	}
	if val.String() != expected {
		t.Errorf("attribute %q: expected %q, got %q", key, expected, val.String())
	}
}

func assertAttrInt(t *testing.T, rec capturedRecord, key string, expected int64) {
	t.Helper()
	val, ok := rec.Attrs[key]
	if !ok {
		t.Errorf("expected attribute %q", key)
		return
	}
	if val.Int64() != expected {
		t.Errorf("attribute %q: expected %d, got %d", key, expected, val.Int64())
	}
}
