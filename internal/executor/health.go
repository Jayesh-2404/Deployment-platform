package executor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HealthChecker decides whether a candidate container is actually able to serve
// traffic. This is the gate that stops a broken deploy from ever becoming the
// live one, so it is an interface: the Docker path probes over HTTP here, and
// tests probe against httptest servers.
type HealthChecker interface {
	// Wait blocks until baseURL+path answers with a 2xx/3xx, or returns an
	// error. It must respect ctx and the timeout.
	Wait(ctx context.Context, baseURL string, path string, timeout time.Duration) error
}

// HTTPHealthChecker polls a path until it succeeds.
//
// Default behaviour matches how orchestrators behave: any status below 400
// counts as healthy, and the container gets the full timeout to come up because
// a cold start plus framework boot is often tens of seconds.
type HTTPHealthChecker struct {
	Client *http.Client
	// Interval between attempts. Defaults to 2s.
	Interval time.Duration
	// Attempts caps the retries. When zero it is derived from Timeout/Interval.
	Attempts int
}

func NewHTTPHealthChecker() *HTTPHealthChecker {
	return &HTTPHealthChecker{
		Client:   &http.Client{Timeout: 5 * time.Second},
		Interval: 2 * time.Second,
	}
}

func (h *HTTPHealthChecker) Wait(ctx context.Context, baseURL string, path string, timeout time.Duration) error {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	target := strings.TrimRight(baseURL, "/") + path

	interval := h.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	attempts := h.Attempts
	if attempts <= 0 {
		attempts = int(timeout/interval) + 1
		if attempts < 1 {
			attempts = 1
		}
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("health check cancelled: %w", ctx.Err())
			case <-time.After(interval):
			}
		}
		if ctx.Err() != nil {
			return fmt.Errorf("health check cancelled: %w", ctx.Err())
		}
		if time.Now().After(deadline) {
			break
		}

		status, err := probe(ctx, client, target)
		switch {
		case err != nil:
			lastErr = err
		case status >= 200 && status < 400:
			return nil
		default:
			lastErr = fmt.Errorf("health endpoint returned status %d", status)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no response")
	}
	return fmt.Errorf("health check failed for %s after %s: %w", target, timeout, lastErr)
}

func probe(ctx context.Context, client *http.Client, target string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "deploy-platform-healthcheck/1.0")
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode, nil
}

// loopbackURL builds the base URL used to probe a container published on a
// loopback port.
func loopbackURL(port int) string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}
