# lootlog

Wide event logging for Go, built on `log/slog`.

Instead of scattering many small log lines throughout a request, lootlog collects attributes as the request flows through middleware, handlers, and services, then emits a single "wide" log line at the end containing all accumulated context. This produces logs that are far easier to query and correlate in observability tools.

lootlog is designed to complement `log/slog`, not replace it — it sits alongside slog for the specific case where you want to accumulate attributes across a request and emit them as a single wide event. Keep using slog for traditional log lines, and reach for lootlog when you want one rich event per unit of work.

Zero external dependencies — built entirely on the standard library.

## Why wide events?

With traditional structured logging, a single request often produces many log lines, each carrying only part of the context:

```go
slog.InfoContext(ctx, "order started", "user_id", userID)
// ... load cart ...
slog.InfoContext(ctx, "cart loaded", "item_count", n)
// ... authorize payment ...
slog.InfoContext(ctx, "payment authorized", "amount", total)
// ... persist order ...
slog.InfoContext(ctx, "order completed", "order_id", orderID)
```

To answer a single question — *what happened in order X?* — you have to stitch those rows back together in your logging tool, relying on a request or trace id being present on every line.

With lootlog, you accumulate attributes on a logger that flows through the operation and emit them as a single event at the end:

```go
wel := lootlog.New(logger)
wel.AddString("user_id", userID)
wel.AddInt("item_count", n)
wel.AddFloat64("amount", total)
wel.AddString("order_id", orderID)
wel.Emit(ctx, slog.LevelInfo, "order completed")
```

One record per operation, containing everything you collected along the way — one row to query, no joins.

## Status

**Experimental.** This package is in early development and its API may change or evolve as the design settles. Pin to a specific version if you depend on it.

## Installation

```
go get github.com/aranw/lootlog
```

## Quick Start

```go
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aranw/lootlog"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	wel := lootlog.New(logger)
	wel.AddString("user_id", "usr_123")
	wel.AddString("action", "checkout")
	wel.AddInt("item_count", 3)
	wel.AddFloat64("total", 59.97)
	wel.Emit(context.Background(), slog.LevelInfo, "order completed")

	// Output: single JSON log line with all attributes + duration_ms
}
```

## HTTP Middleware

The `lootloghttp` subpackage provides middleware that automatically creates a wide event logger for each request, captures method, path, status code, outcome, and duration.

```go
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/aranw/lootlog/lootloghttp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /hello", helloHandler)

	wrapped := lootloghttp.Middleware(logger)(mux)
	http.ListenAndServe(":8080", wrapped)
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("hello"))
}
```

Each request produces a single log line like:

```json
{
  "level": "INFO",
  "msg": "http request",
  "method": "GET",
  "path": "/hello",
  "status_code": 200,
  "outcome": "success",
  "duration_ms": 0.123
}
```

## Adding Context Inside Handlers

Use `FromContext` to retrieve the logger and add attributes from anywhere in the request lifecycle:

```go
func userHandler(w http.ResponseWriter, r *http.Request) {
	wel := lootlog.FromContext(r.Context())

	user, err := loadUser(r)
	if err != nil {
		wel.AddError(err, "auth_error")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	wel.AddObject("user",
		slog.String("id", user.ID),
		slog.String("role", user.Role),
		slog.Bool("premium", user.Premium),
	)

	// All these attributes appear in the single log line emitted by the middleware,
	// nested under a "user" key:
	//   "user": {"id": "usr_123", "role": "admin", "premium": true}
	w.Write([]byte("ok"))
}
```

## Capturing Errors

`AddError` groups an error under an `"error"` key with `type` and `message`, plus any extra attributes you pass:

```go
if err := chargeCard(ctx, total); err != nil {
    wel := lootlog.FromContext(ctx)
    wel.AddError(err, "payment_failed",
        slog.String("provider", "stripe"),
        slog.String("decline_code", declineCode(err)),
    )
    http.Error(w, "payment declined", http.StatusPaymentRequired)
    return
}
```

The emitted event contains a structured error group:

```json
{
  "error": {
    "type": "payment_failed",
    "message": "card declined: insufficient_funds",
    "provider": "stripe",
    "decline_code": "insufficient_funds"
  }
}
```

Because the middleware chooses its log level from the HTTP status code (Warn for 4xx, Error for 5xx), a failed request is already emitted at an appropriate level — `AddError` just makes sure the error details are structured rather than baked into the message string.

## Outside of HTTP

Nothing in the core package is HTTP-specific. The same pattern works for background jobs, workers, CLI commands, or anywhere you want one rich event per unit of work:

```go
func processJob(ctx context.Context, logger *slog.Logger, job Job) {
    wel := lootlog.New(logger)
    ctx = lootlog.WithContext(ctx, wel)

    wel.AddString("job_id", job.ID)
    wel.AddString("job_type", job.Type)

    if err := runJob(ctx, job); err != nil {
        wel.AddError(err, "job_failed")
        wel.Emit(ctx, slog.LevelError, "job finished")
        return
    }

    wel.Emit(ctx, slog.LevelInfo, "job finished")
}

func runJob(ctx context.Context, job Job) error {
    wel := lootlog.FromContext(ctx)
    wel.AddInt("attempts", 1)
    // ... do work, add more context as you go ...
    return nil
}
```

Inner functions reach the logger via `FromContext` and contribute attributes without needing to know where the event will be emitted — only the outermost call site calls `Emit`.

## API Reference

### Core (`lootlog`)

| Function / Method | Description |
|---|---|
| `New(logger *slog.Logger) *WideEventLogger` | Create a new wide event logger |
| `WithContext(ctx, wel) context.Context` | Store logger in context |
| `FromContext(ctx) *WideEventLogger` | Retrieve logger from context (nil if absent) |
| `wel.Add(key, slog.Value)` | Add a generic attribute |
| `wel.AddString(key, value)` | Add a string attribute |
| `wel.AddInt(key, value)` | Add an int attribute |
| `wel.AddInt64(key, value)` | Add an int64 attribute |
| `wel.AddBool(key, value)` | Add a bool attribute |
| `wel.AddFloat64(key, value)` | Add a float64 attribute |
| `wel.AddDuration(key, value)` | Add a `time.Duration` attribute |
| `wel.AddTime(key, value)` | Add a `time.Time` attribute |
| `wel.AddAny(key, value)` | Add an attribute via `slog.AnyValue` |
| `wel.AddObject(key, attrs...)` | Add a nested group of attributes |
| `wel.AddError(err, errType, attrs...)` | Add a structured error group |
| `wel.Emit(ctx, level, msg)` | Emit all accumulated attributes as one log event |

All methods are nil-safe and thread-safe.

**Duplicate keys:** Attributes are appended, not deduplicated. Adding the same key more than once produces duplicate keys in the emitted log event, consistent with `log/slog` semantics. How duplicates are rendered depends on your slog handler.

**Calling Emit more than once:** Each call emits a separate log event containing all attributes accumulated so far, with an independent `duration_ms`. Attributes added after an Emit call will appear in subsequent Emit calls alongside earlier attributes.

### HTTP Middleware (`lootloghttp`)

| Function | Description |
|---|---|
| `Middleware(logger) func(http.Handler) http.Handler` | Standard middleware that logs wide events per request |

The middleware automatically records `method`, `path`, `status_code`, `outcome`, and `duration_ms`. It sets the log level based on the status code: Info for 2xx/3xx, Warn for 4xx, Error for 5xx.

## License

MIT
