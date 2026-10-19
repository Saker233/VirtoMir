// Package compliance provides host hardening posture evaluation (CIS Linux / STIG
// virtualization host subset), FIPS mode detection, and audit verification (ADR-0064).
package compliance

import (
	"context"
	"os"
	"sync"
	"time"
)

// Scanner coordinates evaluation of compliance checks for a host and maintains a short-lived cache.
type Scanner struct {
	reader   *Reader
	mu       sync.RWMutex
	cached   HostCompliance
	lastScan time.Time
}

// NewScanner constructs a new compliance scanner using the provided reader.
// If r is nil, a default reader rooted at "/" is created.
func NewScanner(r *Reader) *Scanner {
	if r == nil {
		r = NewReader("/")
	}
	return &Scanner{reader: r}
}

// Reader returns the underlying reader.
func (s *Scanner) Reader() *Reader {
	return s.reader
}

// Cached returns the cached evaluation result if a scan was completed within the last 60 seconds.
func (s *Scanner) Cached() (HostCompliance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.lastScan.IsZero() || time.Since(s.lastScan) > 60*time.Second {
		return HostCompliance{}, false
	}
	return s.cached, true
}

// Scan executes all compliance checks sequentially, aggregates counts, computes
// the compliance score percentage ((PassedCount * 100) / TotalCount), caches the result,
// and returns HostCompliance. If force is true, cached results are bypassed.
func (s *Scanner) Scan(ctx context.Context, hostID, hostName string, force ...bool) HostCompliance {
	isForced := len(force) > 0 && force[0]
	if !isForced {
		if cached, ok := s.Cached(); ok {
			return cached
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !isForced && !s.lastScan.IsZero() && time.Since(s.lastScan) <= 60*time.Second {
		return s.cached
	}

	checks := AllChecks()
	results := make([]CheckResult, 0, len(checks))
	var passed, failed, warn int

	for _, check := range checks {
		res := check(ctx, s.reader)
		results = append(results, res)
		switch res.Status {
		case StatusPass:
			passed++
		case StatusFail:
			failed++
		case StatusWarn:
			warn++
		}
	}

	total := len(results)
	score := 0
	if total > 0 {
		score = (passed * 100) / total
	}

	if hostID == "" {
		hostID = "local"
	}
	if hostName == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			hostName = h
		} else {
			hostName = "localhost"
		}
	}

	now := time.Now().UTC()
	comp := HostCompliance{
		HostID:       hostID,
		HostName:     hostName,
		EvaluatedAt:  now,
		Score:        score,
		PassedCount:  passed,
		FailedCount:  failed,
		WarningCount: warn,
		TotalCount:   total,
		Results:      results,
	}

	s.cached = comp
	s.lastScan = now
	return comp
}

// ScanForced executes a fresh scan bypassing the 60s TTL cache.
func (s *Scanner) ScanForced(ctx context.Context, hostID, hostName string) HostCompliance {
	return s.Scan(ctx, hostID, hostName, true)
}
