package allstak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// Ingest endpoint paths — relative to the resolved host.
const (
	pathErrors       = "/ingest/v1/errors"
	pathLogs         = "/ingest/v1/logs"
	pathHTTPRequests = "/ingest/v1/http-requests"
	pathDBQueries    = "/ingest/v1/db"
	pathSpans        = "/ingest/v1/spans"
	pathHeartbeat    = "/ingest/v1/heartbeat"
)

// httpTransport is the low-level HTTP ingest transport. It knows how to
// serialize a payload, attach auth, and retry on transient failures. It
// does NOT batch — batching happens in the queue/worker layer above.
//
// The transport is safe for concurrent use. http.Client is already
// goroutine-safe, and this struct holds no mutable state post-construction.
type httpTransport struct {
	host       string
	apiKey     string
	httpClient *http.Client
	maxRetries int
	debug      bool
}

// newHTTPTransport constructs a transport wired to the given host. A nil
// httpClient uses a fresh default client with the configured timeout.
func newHTTPTransport(host, apiKey string, timeout time.Duration, maxRetries int, debug bool) *httpTransport {
	return &httpTransport{
		host:       host,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
		maxRetries: maxRetries,
		debug:      debug,
	}
}

// send marshals payload to JSON and POSTs it to host+path with the
// X-AllStak-Key header. Transient failures (network errors, 5xx, 429) are
// retried with exponential backoff + jitter up to maxRetries times. A 4xx
// other than 429 is treated as permanent and returned immediately. Success
// is any 2xx. The context can cancel any outstanding retry.
func (t *httpTransport) send(ctx context.Context, path string, payload any) error {
	if t.apiKey == "" {
		// No-op mode — silently drop so the SDK is safe to include without
		// configuration in tests and local scripts.
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("allstak: marshal payload: %w", err)
	}

	url := t.host + path
	var lastErr error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if attempt > 0 {
			// Exponential backoff: 100ms, 200ms, 400ms, ... capped at 5s
			// with ±25% jitter to avoid thundering herds.
			base := time.Duration(100*(1<<uint(attempt-1))) * time.Millisecond
			if base > 5*time.Second {
				base = 5 * time.Second
			}
			jitter := time.Duration(rand.Int63n(int64(base / 2)))
			sleep := base - base/4 + jitter
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("allstak: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AllStak-Key", t.apiKey)
		req.Header.Set("User-Agent", "allstak-go/"+sdkVersion)

		resp, err := t.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("allstak: http post %s: %w", path, err)
			if t.debug {
				fmt.Fprintf(stderrWriter, "[allstak] transport attempt %d failed: %v\n", attempt+1, err)
			}
			continue
		}

		// Drain and close the body regardless of outcome so the connection
		// can be reused by keep-alive.
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		// 4xx (except 429) is permanent — no point retrying an invalid key
		// or a malformed payload.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return fmt.Errorf("allstak: ingest %s returned %d: %s", path, resp.StatusCode, truncate(string(respBody), 300))
		}

		lastErr = fmt.Errorf("allstak: ingest %s returned %d", path, resp.StatusCode)
		if t.debug {
			fmt.Fprintf(stderrWriter, "[allstak] transport attempt %d got %d: %s\n", attempt+1, resp.StatusCode, truncate(string(respBody), 200))
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("allstak: ingest %s exhausted %d retries", path, t.maxRetries)
	}
	return lastErr
}

// truncate returns s clipped to n runes with a "..." suffix if clipped.
// Used purely for bounded error messages; callers should never rely on it
// for safety-critical truncation.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stderrWriter is indirected so tests can capture debug output if needed.
// It's a package-level var instead of a constructor argument because debug
// output is inherently a side channel.
var stderrWriter io.Writer = io.Discard
