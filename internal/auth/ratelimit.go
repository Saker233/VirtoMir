package auth

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter implements a token-bucket rate limiter per client IP and /24 subnet.
type RateLimiter struct {
	mu      sync.Mutex
	limit   int           // allowed requests per window
	window  time.Duration // sliding window duration
	clients map[string][]time.Time
	subnets map[string][]time.Time
	now     func() time.Time
}

// NewRateLimiter returns a rate limiter allowing up to limit requests per window per IP.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		clients: make(map[string][]time.Time),
		subnets: make(map[string][]time.Time),
		now:     time.Now,
	}
}

// Allow checks if the given remote address is allowed to proceed.
// If allowed, it records the request and returns true, 0.
// If denied, it returns false along with the retry-after duration in seconds.
func (rl *RateLimiter) Allow(remote string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	ip := remoteIP(remote)
	now := rl.now()
	cutoff := now.Add(-rl.window)
	if len(rl.clients) > sweepAbove {
		sweep(rl.clients, cutoff)
		sweep(rl.subnets, cutoff)
	}

	// Clean up and check IP
	times := rl.clients[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		oldest := valid[0]
		retryAfter := int(oldest.Add(rl.window).Sub(now).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		rl.clients[ip] = valid
		return false, retryAfter
	}

	// Also check subnet (/24 for IPv4, /64 for IPv6) with 4x limit
	subnet := subnetKey(ip)
	subTimes := rl.subnets[subnet]
	subValid := subTimes[:0]
	for _, t := range subTimes {
		if t.After(cutoff) {
			subValid = append(subValid, t)
		}
	}

	if len(subValid) >= rl.limit*4 {
		oldest := subValid[0]
		retryAfter := int(oldest.Add(rl.window).Sub(now).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		rl.subnets[subnet] = subValid
		return false, retryAfter
	}

	// Allowed: record
	rl.clients[ip] = append(valid, now)
	rl.subnets[subnet] = append(subValid, now)
	return true, 0
}

// sweepAbove is how many tracked addresses make Allow drop the ones whose window has passed,
// so a scan from many addresses cannot grow the maps without bound.
const sweepAbove = 4096

func sweep(m map[string][]time.Time, cutoff time.Time) {
	for k, times := range m {
		if len(times) == 0 || !times[len(times)-1].After(cutoff) {
			delete(m, k)
		}
	}
}

// Middleware wraps an http.Handler with rate limiting. If rate limited,
// it responds with 429 Too Many Requests and sets the Retry-After header.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter := rl.Allow(r.RemoteAddr)
		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"too many requests, please slow down"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// subnetKey returns the /24 network for IPv4 or /64 for IPv6.
func subnetKey(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	if v4 := ip.To4(); v4 != nil {
		mask := net.CIDRMask(24, 32)
		return v4.Mask(mask).String()
	}
	mask := net.CIDRMask(64, 128)
	return ip.Mask(mask).String()
}
