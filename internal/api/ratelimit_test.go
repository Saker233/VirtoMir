package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
)

func TestAuthRateLimiting(t *testing.T) {
	// Limiter allowing only 2 requests per minute
	limiter := auth.NewRateLimiter(2, 1*time.Minute)
	m := auth.New(stubAuthn{}, func(string) auth.Role { return auth.Admin })
	s := NewServer(Options{
		Auth:        m,
		RateLimiter: limiter,
	})
	h := s.Handler()

	doReq := func() *http.Response {
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"user":"test","password":"secret"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.168.1.50:12345"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}

	// 1st request: allowed
	res1 := doReq()
	if res1.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("first request should not be rate limited")
	}

	// 2nd request: allowed
	res2 := doReq()
	if res2.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("second request should not be rate limited")
	}

	// 3rd request: rate limited!
	res3 := doReq()
	if res3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 Too Many Requests, got %d", res3.StatusCode)
	}
	retryAfter := res3.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("expected Retry-After header on 429 response")
	}
	body, _ := io.ReadAll(res3.Body)
	if !strings.Contains(string(body), "too many login attempts") {
		t.Errorf("expected body to mention rate limit, got %s", string(body))
	}
}
