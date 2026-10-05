package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthCheckerAcceptsAnySuccessStatus(t *testing.T) {
	for _, status := range []int{200, 201, 204, 301, 302, 399} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer server.Close()

			checker := &HTTPHealthChecker{Interval: time.Millisecond}
			if err := checker.Wait(context.Background(), server.URL, "/healthz", time.Second); err != nil {
				t.Errorf("status %d should be healthy, got %v", status, err)
			}
		})
	}
}

func TestHealthCheckerRetriesUntilTheAppBecomesReady(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := &HTTPHealthChecker{Interval: time.Millisecond}
	if err := checker.Wait(context.Background(), server.URL, "/healthz", 2*time.Second); err != nil {
		t.Fatalf("a slow-starting app should become healthy, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 probes, got %d", calls.Load())
	}
}

func TestHealthCheckerFailsAfterExhaustingAttempts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	checker := &HTTPHealthChecker{Interval: time.Millisecond, Attempts: 3}
	err := checker.Wait(context.Background(), server.URL, "/healthz", time.Second)
	if err == nil {
		t.Fatal("expected a failure when the app never becomes healthy")
	}
	if got := err.Error(); !strings.Contains(got, "500") {
		t.Errorf("error should include the last status code, got %q", got)
	}
}

func TestHealthCheckerRequestsTheConfiguredPath(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := &HTTPHealthChecker{Interval: time.Millisecond}
	if err := checker.Wait(context.Background(), server.URL, "/deep/health", time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if path := <-seen; path != "/deep/health" {
		t.Errorf("probed path = %q, want /deep/health", path)
	}
}

func TestHealthCheckerNormalisesMissingLeadingSlash(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := &HTTPHealthChecker{Interval: time.Millisecond}
	if err := checker.Wait(context.Background(), server.URL, "healthz", time.Second); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if path := <-seen; path != "/healthz" {
		t.Errorf("probed path = %q, want /healthz", path)
	}
}

func TestHealthCheckerHonoursContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	checker := &HTTPHealthChecker{Interval: 50 * time.Millisecond, Attempts: 100}
	err := checker.Wait(ctx, server.URL, "/healthz", 30*time.Second)
	if err == nil {
		t.Fatal("a cancelled context must abort the health check")
	}
}

func TestHealthCheckerFailsWhenNothingIsListening(t *testing.T) {
	// Port 1 on loopback is reserved and nothing listens there.
	checker := &HTTPHealthChecker{Interval: time.Millisecond, Attempts: 2}
	err := checker.Wait(context.Background(), "http://127.0.0.1:1", "/", time.Second)
	if err == nil {
		t.Fatal("expected a failure when the container is not reachable")
	}
}

func TestLoopbackURL(t *testing.T) {
	if got := loopbackURL(32768); got != "http://127.0.0.1:32768" {
		t.Errorf("loopbackURL = %q, want http://127.0.0.1:32768", got)
	}
}
