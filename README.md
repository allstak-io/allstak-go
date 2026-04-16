# allstak-go

[![Go Reference](https://pkg.go.dev/badge/github.com/allstak/allstak-go.svg)](https://pkg.go.dev/github.com/allstak/allstak-go)
[![Go Version](https://img.shields.io/badge/go-%3E%3D1.23-00ADD8?logo=go)](https://go.dev)

The official Go SDK for [AllStak](https://allstak.dev) — the all-in-one
observability platform that captures errors, logs, HTTP requests, database
queries, traces, and cron job runs from your Go services.

One package, one minute of setup, everything on one dashboard.

## What you get

- **Errors** — panics, wrapped errors, manual captures, stack traces, grouped automatically
- **Logs** — structured `Info`/`Warn`/`Error`/`Debug` with trace correlation
- **Inbound HTTP** — method, path, status, duration, user context, trace ID per request
- **Outbound HTTP** — drop-in `http.RoundTripper` wrapper captures success + network failures
- **Database** — GORM plugin with normalized SQL, durations, rows affected, success/error status
- **Traces** — distributed trace ID + span ID propagation over a `X-AllStak-Trace-Id` header
- **Cron monitors** — helper for `robfig/cron` (or any `func()` scheduler) that sends heartbeats

## Install

```bash
go get github.com/allstak/allstak-go
```

Integrations that pull in third-party frameworks are separate nested modules
so you only download what you use:

```bash
go get github.com/allstak/allstak-go/integrations/allstakgorm   # GORM
go get github.com/allstak/allstak-go/integrations/allstakgin    # Gin
```

## 60-second setup

```go
package main

import (
    "context"
    "log"
    "net/http"
    "time"

    allstak "github.com/allstak/allstak-go"
)

func main() {
    client := allstak.New(allstak.Config{
        APIKey:      "ask_live_xxxxxxxxxxxxxxxxxxxxx",
        Environment: "production",
        Release:     "v1.2.3",
        ServiceName: "billing-api",
    })
    defer client.Close(context.Background())

    mux := http.NewServeMux()
    mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
        w.Write([]byte("hi"))
    })

    // One line and every inbound request is captured, panics are recovered,
    // and a trace ID is propagated through the request context.
    handler := allstak.Middleware(client)(mux)

    log.Fatal((&http.Server{
        Addr:         ":8080",
        Handler:      handler,
        ReadTimeout:  10 * time.Second,
        WriteTimeout: 10 * time.Second,
    }).ListenAndServe())
}
```

That's it. Open the AllStak dashboard and you'll see the `/hello` request
appear in the **Requests** view within a few seconds.

## First error in under a minute

```go
mux.HandleFunc("/charge", func(w http.ResponseWriter, r *http.Request) {
    if err := charge(r.Context()); err != nil {
        client.CaptureException(r.Context(), err)
        http.Error(w, "charge failed", http.StatusInternalServerError)
        return
    }
    w.Write([]byte("ok"))
})
```

`CaptureException` extracts a stack trace, enriches the payload with any
user/request/trace context stored on the ctx, and ships it to the dashboard.

## Configuration

```go
allstak.Config{
    // Required — project-scoped ingest key from the dashboard.
    APIKey: "ask_live_xxxxxxxxxxxxxxxxxxxxx",

    // Optional — free-form deployment tag.
    Environment: "production",  // default: "production"

    // Optional — build identifier, usually a git SHA or semver.
    Release: "v1.2.3",

    // Optional — service name shown in the dashboard.
    ServiceName: "billing-api",  // default: basename of os.Args[0]

    // Optional — verbose internal logs to stderr.
    Debug: false,

    // Optional — background worker tuning (defaults are sensible).
    FlushInterval:  2 * time.Second,
    BatchSize:      50,
    QueueCapacity:  1000,
    MaxRetries:     3,
    RequestTimeout: 5 * time.Second,
}
```

**Host override**: the SDK targets the production ingest host by default
(`INGEST_HOST` constant). For self-hosted deployments or local development,
set `ALLSTAK_HOST=http://your-host:8080` in the environment. There is no
`Host` field on `Config` — customers should never have to know which URL
their events go to.

**No-op mode**: an empty `APIKey` puts the SDK into silent no-op mode. This
means libraries can wire AllStak in safely without forcing every consumer
to configure a key.

## Framework integrations

### Chi (or any `net/http`)

```go
import (
    allstak "github.com/allstak/allstak-go"
    "github.com/go-chi/chi/v5"
)

r := chi.NewRouter()
r.Use(allstak.Middleware(client))
r.Get("/tasks", handleListTasks)
```

### Gin

```go
import (
    allstak "github.com/allstak/allstak-go"
    allstakgin "github.com/allstak/allstak-go/integrations/allstakgin"
    "github.com/gin-gonic/gin"
)

r := gin.New()
r.Use(allstakgin.Middleware(client))
r.GET("/tasks", handleListTasks)
```

Both middlewares:
- stamp a trace ID + span context on the request context
- install a mutable request-state bag so late-arriving user info from auth
  middleware propagates to any panic captured further down the stack
- recover panics and capture them as fatal errors
- record one inbound HTTP event per request with the resolved route pattern

### Database (GORM)

```go
import (
    allstakgorm "github.com/allstak/allstak-go/integrations/allstakgorm"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

db, _ := gorm.Open(sqlite.Open("app.db"), &gorm.Config{})
allstakgorm.Instrument(db, client, allstakgorm.WithDatabaseName("app"))
```

Every query through `db` is captured with normalized SQL (literals replaced
with `?`), a stable group hash, duration, rows affected, and success/error
status. Queries made via `db.WithContext(ctx)` are automatically correlated
to the active trace.

Supported GORM callbacks: `Create`, `Query`, `Update`, `Delete`, `Row`, `Raw`.

### Outbound HTTP

```go
import (
    "net/http"
    allstak "github.com/allstak/allstak-go"
)

httpClient := &http.Client{
    Transport: allstak.NewTransport(client, nil), // nil = http.DefaultTransport
}
```

Every RoundTrip captures:
- method, host, path, status code, duration
- network failures (status `0`) with a per-host error fingerprint
- the active trace ID + span ID + user ID from `req.Context()`
- automatic `X-AllStak-Trace-Id` / `X-AllStak-Span-Id` header propagation
  so the downstream service can link its spans to yours

### Cron jobs

```go
import (
    "context"
    allstakcron "github.com/allstak/allstak-go/integrations/allstakcron"
    "github.com/robfig/cron/v3"
)

c := cron.New()

c.AddFunc("@every 5m", allstakcron.Wrap(client, "daily-report", func(ctx context.Context) error {
    return runDailyReport(ctx)
}))

c.AddFunc("@every 10m", allstakcron.Wrap(client, "sync-accounts", func(ctx context.Context) error {
    return syncAccounts(ctx)
}))

c.Start()
```

Each run sends a heartbeat (`success` or `failed`), captures panics as fatal
errors, and emits a span so the job shows up on the Traces page too. Monitors
are auto-created in the dashboard on first heartbeat — no pre-registration
required.

## Manual capture

```go
// Structured log → dashboard Logs view
client.Info(ctx, "user signed up", allstak.F("plan", "pro"), allstak.F("userId", 42))

// Error with stack trace → dashboard Errors view
client.CaptureException(ctx, fmt.Errorf("checkout failed: %w", err))

// Message with explicit level
client.CaptureExceptionWithLevel(ctx, err, "warn")

// Plain text notable event (no stack trace)
client.CaptureMessage(ctx, "info", "cache warmed")

// Manual span
ctx, finish := client.StartSpan(ctx, "payments.charge")
defer func() { finish(err) }()
```

## User & request context

```go
// In your auth middleware, AFTER resolving the JWT / session:
ctx := allstak.WithUser(r.Context(), &allstak.User{
    ID:    user.ID,
    Email: user.Email,
    IP:    clientIP(r),
})
next.ServeHTTP(w, r.WithContext(ctx))
```

Any subsequent `CaptureException`, `Info`, `Warn`, or inbound HTTP record
is automatically stamped with that user. This works *even if* your auth
middleware runs AFTER the AllStak middleware — the SDK installs a mutable
request-state bag at the top of each request specifically so downstream
user info is visible to the outer panic handler.

## What gets auto-captured

| Integration                 | Events | Dashboard view |
|---|---|---|
| `allstak.Middleware`        | inbound HTTP + panics + trace ctx | Requests, Errors, Traces |
| `allstak.NewTransport`      | outbound HTTP success + failure | Requests, Traces |
| `allstakgorm.Instrument`    | every GORM query | Database, Traces |
| `allstakcron.Wrap/RunJob`   | heartbeat + fail capture + span | Cron monitors, Traces, Errors |
| `client.Info/Warn/Error`    | structured log | Logs |
| `client.CaptureException`   | error with stack trace | Errors |

## Dashboard mapping

- `CaptureError` / `CaptureException` → **Errors** page (grouped by fingerprint)
- `CaptureLog` / `Info`/`Warn`/`Error`  → **Logs** page (filter by level, service, user)
- `CaptureHTTPRequest` (direction=inbound)  → **Requests** page
- `CaptureHTTPRequest` (direction=outbound) → **Requests** page (filtered)
- `CaptureDBQuery`                          → **Database** page (grouped by query hash)
- `CaptureSpan`                             → **Traces** page
- `SendHeartbeat`                           → **Cron monitors** page

## Production notes

- **Thread safety**: `Client` is safe for concurrent use from any number of
  goroutines. Create one at program start and reuse it.
- **Back pressure**: queues are bounded (default 1000 per stream). When full,
  the oldest event is dropped to make room — the most recent (usually most
  relevant) failure is always preserved. See `client.Stats()` for counters.
- **Graceful shutdown**: always call `client.Close(ctx)` or
  `client.Flush(ctx)` before your program exits. Buffered events are
  otherwise lost.
- **No goroutine leaks**: `Close` stops every background worker and waits
  for in-flight sends to finish.
- **Retries**: failed ingests retry up to `MaxRetries` times with
  exponential backoff + jitter (100ms → 5s cap, ±25% jitter). 4xx other
  than 429 is treated as permanent and surfaced immediately.
- **Zero panics into host**: the SDK never panics into user code. A broken
  transport just drops events and increments the `dropped` counter.

## Troubleshooting

**Nothing appears in the dashboard**

1. Verify the API key: `curl -H "X-AllStak-Key: ask_..." https://ingest.allstak.dev/healthz`
2. Enable debug mode: `Debug: true` in `Config` — you'll see transport
   attempts, retries, and failures on stderr.
3. Confirm the project: the API key is project-scoped, so events land in
   the project that owns the key.
4. Check the queue: `client.Stats()` shows `Sent` / `Dropped` / `Failed`.
5. Flush before exit: short-lived scripts must call `client.Close(ctx)`
   or `client.Flush(ctx)` before the process terminates.

**Everything is captured as 401 / 403 / 404 with no user**

Your auth middleware probably runs *before* returning from the handler for
those paths, so there's no authenticated user at the time the inbound HTTP
event is recorded. That's expected and correct — the user field is only
populated when auth has resolved a principal.

**DB events show 0ms durations**

The `before` callback didn't fire (unlikely for GORM, possible for custom
wrappers). The SDK still emits the event so the query shows up, just with
zero duration. If you see this consistently, open an issue.

## License

Apache 2.0 — see [LICENSE](LICENSE).
