package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/compliance"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func TestGetComplianceChecks(t *testing.T) {
	h, _, _ := rbacServer(t)
	view := signIn(t, h, "view")

	rec := do(h, "GET", "/api/v1/compliance/checks", "", view)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var res compliance.HostCompliance
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res.TotalCount <= 0 || len(res.Results) == 0 {
		t.Fatalf("expected check results, got %d results", len(res.Results))
	}
	if res.Score < 0 || res.Score > 100 {
		t.Fatalf("unexpected score: %d", res.Score)
	}

	// Also verify ?refresh=1 forces scan
	recRefresh := do(h, "GET", "/api/v1/compliance/checks?refresh=1", "", view)
	if recRefresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want 200", recRefresh.Code)
	}
}

func TestRunComplianceChecks(t *testing.T) {
	h, _, log := rbacServer(t)
	op := signIn(t, h, "op")
	view := signIn(t, h, "view")

	// Operator returns 200 and records activity entry
	rec := do(h, "POST", "/api/v1/compliance/checks/run", "", op)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator run status = %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var res compliance.HostCompliance
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(res.Results) == 0 {
		t.Errorf("expected results from fresh scan")
	}

	// Verify activity entry was recorded
	entries := log.List(10, activity.Filter{})
	var found bool
	for _, e := range entries {
		if e.Path == "/api/v1/compliance/checks/run" && e.User == "op" && e.Status == http.StatusOK {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected activity log entry for operator run, entries: %+v", entries)
	}

	// Viewer returns 403 Forbidden
	recView := do(h, "POST", "/api/v1/compliance/checks/run", "", view)
	if recView.Code != http.StatusForbidden {
		t.Fatalf("viewer status = %d, want 403", recView.Code)
	}
}

func TestGetFIPSInfo(t *testing.T) {
	expected := compliance.FIPSInfo{
		KernelEnabled: true,
		Enforced:      true,
		TLSMinVersion: "TLS 1.2",
		CipherSuites:  []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		Curves:        []string{"P-256"},
		Description:   "FIPS mode enabled: restricting TLS to FIPS 140 approved cipher suites (experimental, not tested on hardware)",
	}

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"view": auth.Viewer}[u]
	})
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		Auth:     m,
		Version:  "test",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		FIPSInfo: expected,
	})

	h := srv.Handler()
	view := signIn(t, h, "view")

	rec := do(h, "GET", "/api/v1/compliance/fips", "", view)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var res compliance.FIPSInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !res.Enforced || !res.KernelEnabled {
		t.Fatalf("unexpected fips response: %+v", res)
	}
	if res.TLSMinVersion != "TLS 1.2" {
		t.Fatalf("expected TLS 1.2, got %q", res.TLSMinVersion)
	}
	if len(res.CipherSuites) != 1 || res.CipherSuites[0] != "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256" {
		t.Fatalf("unexpected cipher suites: %v", res.CipherSuites)
	}
}

func TestComplianceSummary(t *testing.T) {
	h, _, _ := rbacServer(t)
	view := signIn(t, h, "view")

	rec := do(h, "GET", "/api/v1/compliance/summary", "", view)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary status = %d, want 200", rec.Code)
	}

	var sum ComplianceSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if sum.TotalChecks <= 0 {
		t.Errorf("expected totalChecks > 0, got %d", sum.TotalChecks)
	}
	if !sum.AuditValid {
		t.Errorf("expected auditValid == true on clean log")
	}
	body := rec.Body.String()
	if strings.Contains(body, `"totalCount"`) || strings.Contains(body, `"auditLogValid"`) {
		t.Errorf("summary must not carry duplicate legacy fields: %s", body)
	}
}

// TestComplianceChecksConcurrent exercises the scanner fallback init from many
// goroutines at once; run under -race it catches data races on the assignment.
func TestComplianceChecksConcurrent(t *testing.T) {
	h, _, _ := rbacServer(t)
	view := signIn(t, h, "view")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := do(h, "GET", "/api/v1/compliance/checks?refresh=1", "", view)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent checks status = %d, want 200", rec.Code)
			}
		}()
	}
	wg.Wait()
}

func TestVerifyAuditLogIntact(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.jsonl")
	acts, err := activity.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}

	// Add a few entries to form a valid hash chain
	for i := 1; i <= 5; i++ {
		_ = acts.Add(activity.Entry{
			User:   "admin",
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/resource/%d", i),
			Status: 200,
		})
	}

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"op": auth.Operator, "view": auth.Viewer}[u]
	})
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		Auth:     m,
		Activity: acts,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h := srv.Handler()
	op := signIn(t, h, "op")

	rec := do(h, "POST", "/api/v1/activity/verify", "", op)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status = %d, want 200; body=%s", rec.Code, rec.Body)
	}

	var res activity.VerificationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal verification result: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid = true on intact log, got false, reason: %s", res.Reason)
	}
	if res.VerifiedCount < 5 {
		t.Errorf("expected at least 5 verified entries, got %d", res.VerifiedCount)
	}
}

func TestVerifyAuditLogTampered(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.jsonl")
	acts, err := activity.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		_ = acts.Add(activity.Entry{
			User:   fmt.Sprintf("user%d", i),
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/resource/%d", i),
			Status: 200,
		})
	}

	// Tamper entry on disk (line 3, index 2)
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d", len(lines))
	}
	var e activity.Entry
	if err := json.Unmarshal([]byte(lines[2]), &e); err != nil {
		t.Fatal(err)
	}
	e.Status = 500 // change without updating hash
	b, _ := json.Marshal(e)
	lines[2] = string(b)
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"op": auth.Operator}[u]
	})
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		Auth:     m,
		Activity: acts,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h := srv.Handler()
	op := signIn(t, h, "op")

	rec := do(h, "POST", "/api/v1/activity/verify", "", op)
	if rec.Code != http.StatusOK {
		t.Fatalf("tampered verify status = %d, want 200", rec.Code)
	}

	var res activity.VerificationResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected valid = false on tampered log")
	}
	if res.TamperedIndex != 2 {
		t.Errorf("expected tamperedIndex = 2, got %d", res.TamperedIndex)
	}
}

func TestExportAuditLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.jsonl")
	acts, err := activity.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 3; i++ {
		_ = acts.Add(activity.Entry{
			User:   "alice",
			Method: "POST",
			Path:   fmt.Sprintf("/api/v1/domains/dom-%d/actions/start", i),
			Status: 200,
		})
	}

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "view": auth.Viewer}[u]
	})
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		Auth:     m,
		Activity: acts,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h := srv.Handler()
	admin := signIn(t, h, "onur")
	view := signIn(t, h, "view")

	// Admin returns 200 with attachment (CSV)
	recCSV := do(h, "GET", "/api/v1/activity/export?format=csv", "", admin)
	if recCSV.Code != http.StatusOK {
		t.Fatalf("admin export csv status = %d, want 200", recCSV.Code)
	}
	cd := recCSV.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "virtomir-activity.csv") {
		t.Errorf("unexpected Content-Disposition: %q", cd)
	}
	bodyCSV := recCSV.Body.String()
	if !strings.Contains(bodyCSV, "# virtoMir audit log export; chain_valid=true") {
		t.Errorf("expected hash metadata comment in CSV: %s", bodyCSV)
	}
	if !strings.Contains(bodyCSV, "dom-1") {
		t.Errorf("expected entries in CSV export: %s", bodyCSV)
	}

	// Verify export action was recorded in activity log
	entries := acts.List(10, activity.Filter{})
	var foundExport bool
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "/api/v1/activity/export") && e.User == "onur" {
			foundExport = true
			break
		}
	}
	if !foundExport {
		t.Errorf("expected export audit activity entry, got entries: %+v", entries)
	}

	// Admin returns 200 with attachment (JSONL)
	recJSONL := do(h, "GET", "/api/v1/activity/export?format=jsonl", "", admin)
	if recJSONL.Code != http.StatusOK {
		t.Fatalf("admin export jsonl status = %d, want 200", recJSONL.Code)
	}
	cdJSONL := recJSONL.Header().Get("Content-Disposition")
	if !strings.Contains(cdJSONL, "attachment") || !strings.Contains(cdJSONL, "virtomir-activity.jsonl") {
		t.Errorf("unexpected Content-Disposition: %q", cdJSONL)
	}
	bodyJSONL := recJSONL.Body.String()
	if !strings.Contains(bodyJSONL, `"_metadata"`) {
		t.Errorf("expected _metadata line in JSONL: %s", bodyJSONL)
	}

	// Viewer returns 403 Forbidden
	recView := do(h, "GET", "/api/v1/activity/export", "", view)
	if recView.Code != http.StatusForbidden {
		t.Fatalf("viewer export status = %d, want 403", recView.Code)
	}
}

// TestExportCSVFormulaInjection verifies that audit fields starting with
// formula trigger characters (=, +, -, @) are neutralized in CSV exports.
func TestExportCSVFormulaInjection(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "activity.jsonl")
	acts, err := activity.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := acts.Add(activity.Entry{
		User:   "=cmd|'/C calc'!A0",
		Method: "POST",
		Path:   "@resource",
		Status: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := acts.Add(activity.Entry{
		User:   "+1",
		Method: "GET",
		Path:   "/api/v1/domains/dom-2/actions/start",
		Status: 200,
	}); err != nil {
		t.Fatal(err)
	}

	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin}[u]
	})
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		Auth:     m,
		Activity: acts,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h := srv.Handler()
	admin := signIn(t, h, "onur")

	rec := do(h, "GET", "/api/v1/activity/export?format=csv", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, dangerous := range []string{`"=cmd`, `"+1`, `"@resource`} {
		if strings.Contains(body, dangerous) {
			t.Errorf("CSV export contains unsanitized formula cell %s: %s", dangerous, body)
		}
	}
	for _, safe := range []string{`'=cmd`, `'+1`, `'@resource`} {
		if !strings.Contains(body, safe) {
			t.Errorf("CSV export missing sanitized cell %s: %s", safe, body)
		}
	}
}

func TestClusterCompliance(t *testing.T) {
	a := newTestNode(t, "alpha", []virt.Domain{})
	b := newTestNode(t, "beta", []virt.Domain{})
	joinNodes(t, a, b)

	if !a.store.Clustered() || !b.store.Clustered() {
		t.Fatal("cluster not formed")
	}

	aComp := compliance.HostCompliance{
		HostID:       a.store.SelfID(),
		HostName:     "alpha",
		Score:        85,
		TotalCount:   10,
		PassedCount:  8,
		WarningCount: 1,
		FailedCount:  1,
		Results:      []compliance.CheckResult{{ID: "c1", Title: "Check 1", Status: compliance.StatusPass}},
	}
	bComp := compliance.HostCompliance{
		HostID:       b.store.SelfID(),
		HostName:     "beta",
		Score:        95,
		TotalCount:   10,
		PassedCount:  9,
		WarningCount: 1,
		FailedCount:  0,
		Results:      []compliance.CheckResult{{ID: "c2", Title: "Check 2", Status: compliance.StatusPass}},
	}

	a.api.complianceScanner.SetCached(aComp)
	b.api.complianceScanner.SetCached(bComp)

	resp, body := a.signIn(t, "view").req("GET", "/api/v1/cluster/compliance", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cluster compliance status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	var aggregated []compliance.HostCompliance
	if err := json.Unmarshal([]byte(body), &aggregated); err != nil {
		t.Fatalf("unmarshal aggregated compliance: %v; raw=%s", err, body)
	}

	if len(aggregated) != 2 {
		t.Fatalf("expected 2 host compliance records, got %d", len(aggregated))
	}

	var foundA, foundB bool
	for _, hc := range aggregated {
		if hc.HostName == "alpha" && hc.Score == 85 {
			foundA = true
		}
		if hc.HostName == "beta" && hc.Score == 95 {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("aggregated compliance missing expected host records: %+v", aggregated)
	}
}
