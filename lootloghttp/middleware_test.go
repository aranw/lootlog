package lootloghttp

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aranw/lootlog"
)

type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]slog.Value
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   make(map[string]slog.Value),
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

func TestResponseWriterCapturesStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	rw.WriteHeader(http.StatusNotFound)

	if rw.statusCode != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rw.statusCode)
	}
}

func TestResponseWriterDefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	if rw.statusCode != http.StatusOK {
		t.Errorf("expected default status %d, got %d", http.StatusOK, rw.statusCode)
	}
}

func TestResponseWriterIgnoresDoubleWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	rw.WriteHeader(http.StatusCreated)
	rw.WriteHeader(http.StatusInternalServerError)

	if rw.statusCode != http.StatusCreated {
		t.Errorf("expected first status %d, got %d", http.StatusCreated, rw.statusCode)
	}
}

func TestResponseWriterWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	n, err := rw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("expected body 'hello', got %q", rec.Body.String())
	}
}

func TestResponseWriterWriteThenWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	// Write implicitly commits 200; a subsequent WriteHeader must be ignored.
	rw.Write([]byte("body"))
	rw.WriteHeader(http.StatusInternalServerError)

	if rw.statusCode != http.StatusOK {
		t.Errorf("expected status %d after Write, got %d", http.StatusOK, rw.statusCode)
	}
}

func TestResponseWriterUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := wrapResponseWriter(rec)

	if rw.Unwrap() != rec {
		t.Error("Unwrap should return the underlying ResponseWriter")
	}

	// http.ResponseController should be able to reach the underlying writer.
	rc := http.NewResponseController(rw)
	if err := rc.Flush(); err != nil {
		t.Errorf("expected Flush via ResponseController to succeed, got %v", err)
	}
}

// hijackableWriter is a fake ResponseWriter that supports http.Flusher and
// http.Hijacker, used to verify Unwrap delegation.
type hijackableWriter struct {
	http.ResponseWriter
	flushed  bool
	hijacked bool
}

func (hw *hijackableWriter) Flush() {
	hw.flushed = true
}

func (hw *hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hw.hijacked = true
	return nil, nil, nil
}

// plainWriter is a minimal ResponseWriter with no optional interfaces.
type plainWriter struct {
	header http.Header
}

func (pw *plainWriter) Header() http.Header        { return pw.header }
func (pw *plainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (pw *plainWriter) WriteHeader(int)             {}

func TestResponseControllerFlush(t *testing.T) {
	hw := &hijackableWriter{ResponseWriter: httptest.NewRecorder()}
	rw := wrapResponseWriter(hw)

	rc := http.NewResponseController(rw)
	if err := rc.Flush(); err != nil {
		t.Fatalf("expected Flush to succeed, got %v", err)
	}
	if !hw.flushed {
		t.Error("expected underlying Flush to be called")
	}
}

func TestResponseControllerHijack(t *testing.T) {
	hw := &hijackableWriter{ResponseWriter: httptest.NewRecorder()}
	rw := wrapResponseWriter(hw)

	rc := http.NewResponseController(rw)
	_, _, err := rc.Hijack()
	if err != nil {
		t.Fatalf("expected Hijack to succeed, got %v", err)
	}
	if !hw.hijacked {
		t.Error("expected underlying Hijack to be called")
	}
}

func TestResponseControllerFlushFailsWhenUnsupported(t *testing.T) {
	pw := &plainWriter{header: http.Header{}}
	rw := wrapResponseWriter(pw)

	rc := http.NewResponseController(rw)
	err := rc.Flush()
	if err == nil {
		t.Fatal("expected Flush to fail on a writer that doesn't support it")
	}
}

func TestResponseControllerHijackFailsWhenUnsupported(t *testing.T) {
	pw := &plainWriter{header: http.Header{}}
	rw := wrapResponseWriter(pw)

	rc := http.NewResponseController(rw)
	_, _, err := rc.Hijack()
	if err == nil {
		t.Fatal("expected Hijack to fail on a writer that doesn't support it")
	}
}

func TestMiddlewareFlushViaResponseController(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		fmt.Fprint(w, "chunk")
		if err := rc.Flush(); err != nil {
			t.Errorf("expected Flush to succeed inside middleware, got %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Flushed != true {
		t.Error("expected response to be flushed")
	}
}

func TestMiddlewareAddsExpectedAttributes(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if h.count() != 1 {
		t.Fatalf("expected 1 log record, got %d", h.count())
	}

	record := h.last()
	if record.Level != slog.LevelInfo {
		t.Errorf("expected Info level, got %v", record.Level)
	}
	if record.Message != "http request" {
		t.Errorf("expected message 'http request', got %q", record.Message)
	}

	assertAttr(t, record, "method", "GET")
	assertAttr(t, record, "path", "/api/test")
	assertAttrInt(t, record, "status_code", 200)
	assertAttr(t, record, "outcome", "success")

	if _, ok := record.Attrs["duration_ms"]; !ok {
		t.Error("expected duration_ms attribute")
	}
}

func TestMiddlewareClientError(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	record := h.last()
	if record.Level != slog.LevelWarn {
		t.Errorf("expected Warn level for 404, got %v", record.Level)
	}
	assertAttr(t, record, "outcome", "client_error")
}

func TestMiddlewareServerError(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodPost, "/explode", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	record := h.last()
	if record.Level != slog.LevelError {
		t.Errorf("expected Error level for 500, got %v", record.Level)
	}
	assertAttr(t, record, "outcome", "server_error")
	assertAttr(t, record, "method", "POST")
}

func TestMiddlewareFromContext(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wel := lootlog.FromContext(r.Context())
		if wel == nil {
			t.Fatal("expected WideEventLogger in context")
		}
		wel.AddString("user_id", "usr_123")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/profile", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	record := h.last()
	assertAttr(t, record, "user_id", "usr_123")
}

func TestMiddlewareDefaultStatus(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't call WriteHeader — should default to 200.
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	record := h.last()
	assertAttrInt(t, record, "status_code", 200)
	assertAttr(t, record, "outcome", "success")
}

func TestMiddlewarePanicLogsServerError(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	}))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()

	defer func() {
		v := recover()
		if v == nil {
			t.Fatal("expected panic to be re-raised")
		}
		if v != "something went wrong" {
			t.Errorf("expected original panic value, got %v", v)
		}

		if h.count() != 1 {
			t.Fatalf("expected 1 log record, got %d", h.count())
		}

		record := h.last()
		if record.Level != slog.LevelError {
			t.Errorf("expected Error level for panic, got %v", record.Level)
		}
		assertAttr(t, record, "panic", "something went wrong")
		assertAttrInt(t, record, "status_code", 500)
		assertAttr(t, record, "outcome", "server_error")
	}()

	handler.ServeHTTP(rec, req)
}

func TestMiddlewarePanicAfterPartialWrite(t *testing.T) {
	h := newCaptureHandler()
	logger := slog.New(h)

	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("mid-response panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/partial", nil)
	rec := httptest.NewRecorder()

	defer func() {
		v := recover()
		if v == nil {
			t.Fatal("expected panic to be re-raised")
		}

		record := h.last()
		// Status was already committed as 200 before the panic, but the
		// middleware overrides the recorded status to 500 for logging.
		assertAttrInt(t, record, "status_code", 500)
		assertAttr(t, record, "panic", "mid-response panic")
		assertAttr(t, record, "outcome", "server_error")
	}()

	handler.ServeHTTP(rec, req)
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
