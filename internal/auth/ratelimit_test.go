package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	now := time.Now()
	rl := NewRateLimiter(3, 1*time.Minute)
	rl.now = func() time.Time { return now }

	// First 3 requests from 192.168.1.50 should succeed
	for i := 1; i <= 3; i++ {
		allowed, _ := rl.Allow("192.168.1.50:54321")
		if !allowed {
			t.Fatalf("request %d should have been allowed", i)
		}
	}

	// 4th request should be denied
	allowed, retryAfter := rl.Allow("192.168.1.50:54321")
	if allowed {
		t.Fatal("4th request should have been rate-limited")
	}
	if retryAfter <= 0 {
		t.Fatalf("expected positive retry-after, got %d", retryAfter)
	}

	// Another IP in a different subnet should succeed
	allowedDiff, _ := rl.Allow("10.0.0.1:12345")
	if !allowedDiff {
		t.Fatal("request from different subnet should have been allowed")
	}

	// Advance time past the window
	now = now.Add(65 * time.Second)
	allowedAfterExpiry, _ := rl.Allow("192.168.1.50:54321")
	if !allowedAfterExpiry {
		t.Fatal("request after expiry should have been allowed")
	}
}

func TestRateLimiter_Middleware(t *testing.T) {
	rl := NewRateLimiter(2, 1*time.Minute)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "1.2.3.4:1234"
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req1)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	// 3rd should get 429
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req1)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header to be set")
	}
}

func TestRateLimiterForgetsOldAddresses(t *testing.T) {
	now := time.Now()
	rl := NewRateLimiter(10000, time.Minute)
	rl.now = func() time.Time { return now }
	for i := 0; i <= sweepAbove; i++ {
		rl.Allow(fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255))
	}
	now = now.Add(2 * time.Minute)
	rl.Allow("192.168.1.1:1")
	if len(rl.clients) != 1 || len(rl.subnets) != 1 {
		t.Errorf("tracked %d addresses and %d subnets after the window passed", len(rl.clients), len(rl.subnets))
	}
}
