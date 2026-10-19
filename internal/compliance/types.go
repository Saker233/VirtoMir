// Package compliance provides host hardening posture evaluation (CIS Linux / STIG
// virtualization host subset), FIPS mode detection, and audit verification (ADR-0064).
package compliance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Severity expresses the security impact of a compliance finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Status represents the evaluation outcome of an individual compliance check.
type Status string

const (
	StatusPass  Status = "pass"
	StatusFail  Status = "fail"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
)

// CheckResult records the evaluated outcome and remediation instructions for a security rule.
type CheckResult struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    string   `json:"category"` // "SSH", "Virtualization", "Firewall", "Kernel", "System"
	Severity    Severity `json:"severity"`
	Status      Status   `json:"status"`
	Detail      string   `json:"detail"`
	Remediation string   `json:"remediation"`
	Benchmark   string   `json:"benchmark"` // "CIS Linux 2.0 / STIG"
}

// HostCompliance aggregates the full compliance posture for a given host.
type HostCompliance struct {
	HostID       string        `json:"hostId"`
	HostName     string        `json:"hostName"`
	EvaluatedAt  time.Time     `json:"evaluatedAt"`
	Score        int           `json:"score"` // 0-100 percentage: (passed * 100) / total
	PassedCount  int           `json:"passedCount"`
	FailedCount  int           `json:"failedCount"`
	WarningCount int           `json:"warningCount"`
	TotalCount   int           `json:"totalCount"`
	Results      []CheckResult `json:"results"`
}

// Reader reads host configuration and runtime facts below Root ("/" on real hosts).
type Reader struct {
	Root string
	// Command runs a helper program (nft, systemctl, timedatectl); nil: exec.CommandContext. Tests replace it.
	Command func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewReader returns a new Reader rooted at root ("/" if empty).
func NewReader(root string) *Reader {
	if root == "" {
		root = "/"
	}
	return &Reader{Root: root}
}

func (r *Reader) path(p string) string {
	clean := strings.TrimPrefix(filepath.ToSlash(p), "/")
	return filepath.Join(r.Root, filepath.FromSlash(clean))
}

// read returns the trimmed content of a small text file ("" if missing).
func (r *Reader) read(p string) string {
	b, err := os.ReadFile(r.path(p))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readInt returns an int64 parsed from a sysfs/procfs file.
func (r *Reader) readInt(p string) (int64, bool) {
	v, err := strconv.ParseInt(r.read(p), 10, 64)
	return v, err == nil
}

// exists returns true if the file or directory exists below Root.
func (r *Reader) exists(p string) bool {
	_, err := os.Stat(r.path(p))
	return err == nil
}

// run executes a host command using r.Command if non-nil or exec.CommandContext.
func (r *Reader) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.Command != nil {
		return r.Command(ctx, name, args...)
	}
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, name, args...).Output()
}
