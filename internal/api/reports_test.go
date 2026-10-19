package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/onurbaydas/virtomir/internal/alerts"
	"github.com/onurbaydas/virtomir/internal/compliance"
	"github.com/onurbaydas/virtomir/internal/reports"
	"github.com/onurbaydas/virtomir/internal/virt"
)

type recordedNotice struct {
	Subject string
	Message string
}

func setupTestReportsServer(t *testing.T, comp compliance.HostCompliance) (*Server, *reports.Store, func(time.Duration) (recordedNotice, bool)) {
	t.Helper()
	dir := t.TempDir()

	var mu sync.Mutex
	var notices []recordedNotice

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Alert struct {
				Subject string `json:"subject"`
				Message string `json:"message"`
			} `json:"alert"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			mu.Lock()
			notices = append(notices, recordedNotice{Subject: payload.Alert.Subject, Message: payload.Alert.Message})
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	alertEngine, err := alerts.Open(filepath.Join(dir, "alerts.json"), nil)
	if err != nil {
		t.Fatalf("alerts.Open: %v", err)
	}
	if err := alertEngine.SetConfig(alerts.Config{
		Channels: []alerts.Channel{
			{
				ID:          "ch-1",
				Name:        "test-channel",
				Kind:        "webhook",
				URL:         ts.URL,
				Format:      "json",
				Enabled:     true,
				MinSeverity: alerts.Warning,
			},
		},
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	repStore, err := reports.Open(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatalf("reports.Open: %v", err)
	}

	scanner := compliance.NewScanner(compliance.NewReader(dir))
	scanner.SetCached(comp)

	backend := &fakeBackend{
		host: virt.HostInfo{Hostname: "host-alpha"},
	}

	ui := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	srv := NewServer(Options{
		Backend:           backend,
		Live:              &fakeLive{},
		UI:                ui,
		Version:           "dev",
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reports:           repStore,
		Alerts:            alertEngine,
		ComplianceScanner: scanner,
		HostName:          "host-alpha",
	})

	waitNotice := func(timeout time.Duration) (recordedNotice, bool) {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			mu.Lock()
			if len(notices) > 0 {
				rec := notices[0]
				notices = notices[1:]
				mu.Unlock()
				return rec, true
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
		}
		return recordedNotice{}, false
	}

	return srv, repStore, waitNotice
}

func TestLocalAndClusterFactsCompliance(t *testing.T) {
	comp := compliance.HostCompliance{
		HostID:       "host-alpha",
		HostName:     "host-alpha",
		Score:        80,
		PassedCount:  4,
		FailedCount:  1,
		WarningCount: 0,
		TotalCount:   5,
		Results: []compliance.CheckResult{
			{ID: "c1", Title: "Check 1", Status: compliance.StatusFail, Severity: compliance.SeverityCritical},
		},
	}

	srv, _, _ := setupTestReportsServer(t, comp)
	ctx := context.Background()

	// localFacts
	lf, err := srv.localFacts(ctx)
	if err != nil {
		t.Fatalf("localFacts error: %v", err)
	}
	if lf.Compliance == nil {
		t.Fatal("expected lf.Compliance to be populated")
	}
	if lf.Compliance.HostID != "host-alpha" || lf.Compliance.Score != 80 {
		t.Errorf("unexpected local compliance: %+v", lf.Compliance)
	}

	// clusterFacts
	cf, err := srv.clusterFacts(ctx)
	if err != nil {
		t.Fatalf("clusterFacts error: %v", err)
	}
	if len(cf.Compliance) != 1 {
		t.Fatalf("cf.Compliance len = %d, want 1", len(cf.Compliance))
	}
	if cf.Compliance[0].HostID != "host-alpha" || cf.Compliance[0].FailedCount != 1 {
		t.Errorf("unexpected cluster compliance: %+v", cf.Compliance[0])
	}
}

func TestRunDueReportSchedules_ComplianceWithFailuresNotifiesEvenWhenNotifyFalse(t *testing.T) {
	comp := compliance.HostCompliance{
		HostID:       "host-alpha",
		HostName:     "host-alpha",
		Score:        50,
		PassedCount:  1,
		FailedCount:  1,
		WarningCount: 0,
		TotalCount:   2,
		Results: []compliance.CheckResult{
			{ID: "ssh-root", Title: "SSH Root Login", Status: compliance.StatusFail, Severity: compliance.SeverityCritical},
			{ID: "sys-kptr", Title: "Kernel Pointers", Status: compliance.StatusPass, Severity: compliance.SeverityMedium},
		},
	}

	srv, repStore, waitNotice := setupTestReportsServer(t, comp)

	// Put schedule with Notify = false
	sc, err := repStore.PutSchedule(reports.Schedule{
		Name:    "Daily Compliance Check",
		Kind:    reports.Compliance,
		Format:  reports.HTML,
		Every:   "daily",
		At:      "00:00",
		Enabled: true,
		Notify:  false,
	})
	if err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}

	ctx := context.Background()
	// Trigger runDueReportSchedules with a past 'since' time
	srv.runDueReportSchedules(ctx, time.Now().Add(-48*time.Hour))

	// Because there are failures (FailedCount > 0), an alert notice MUST be sent even though Notify is false
	notice, ok := waitNotice(2 * time.Second)
	if !ok {
		t.Fatal("expected notification for compliance failures even when Notify=false, but none received")
	}

	if !strings.Contains(notice.Subject, "compliance checks failed") {
		t.Errorf("expected subject to contain 'compliance checks failed', got %q", notice.Subject)
	}
	if !strings.Contains(notice.Message, "compliance score") {
		t.Errorf("expected message to contain report summary, got %q", notice.Message)
	}

	// Verify report exists in store
	list := repStore.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 report generated, got %d", len(list))
	}
	if list[0].ScheduleID != sc.ID {
		t.Errorf("ScheduleID = %q, want %q", list[0].ScheduleID, sc.ID)
	}
}

func TestRunDueReportSchedules_ComplianceNoFailuresDoesNotNotifyWhenNotifyFalse(t *testing.T) {
	comp := compliance.HostCompliance{
		HostID:       "host-alpha",
		HostName:     "host-alpha",
		Score:        100,
		PassedCount:  2,
		FailedCount:  0,
		WarningCount: 0,
		TotalCount:   2,
		Results: []compliance.CheckResult{
			{ID: "c1", Title: "Check 1", Status: compliance.StatusPass, Severity: compliance.SeverityLow},
			{ID: "c2", Title: "Check 2", Status: compliance.StatusPass, Severity: compliance.SeverityMedium},
		},
	}

	srv, repStore, waitNotice := setupTestReportsServer(t, comp)

	// Put schedule with Notify = false
	_, err := repStore.PutSchedule(reports.Schedule{
		Name:    "Clean Compliance Check",
		Kind:    reports.Compliance,
		Format:  reports.HTML,
		Every:   "daily",
		At:      "00:00",
		Enabled: true,
		Notify:  false,
	})
	if err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}

	ctx := context.Background()
	srv.runDueReportSchedules(ctx, time.Now().Add(-48*time.Hour))

	// No failures and Notify=false -> NO notice should be sent
	if notice, ok := waitNotice(500 * time.Millisecond); ok {
		t.Fatalf("unexpected notice received for passing report when Notify=false: %+v", notice)
	}

	// But report was still generated
	list := repStore.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 report generated, got %d", len(list))
	}
}

func TestRunDueReportSchedules_ComplianceNoFailuresNotifiesWhenNotifyTrue(t *testing.T) {
	comp := compliance.HostCompliance{
		HostID:       "host-alpha",
		HostName:     "host-alpha",
		Score:        100,
		PassedCount:  2,
		FailedCount:  0,
		WarningCount: 0,
		TotalCount:   2,
		Results: []compliance.CheckResult{
			{ID: "c1", Title: "Check 1", Status: compliance.StatusPass, Severity: compliance.SeverityLow},
			{ID: "c2", Title: "Check 2", Status: compliance.StatusPass, Severity: compliance.SeverityMedium},
		},
	}

	srv, repStore, waitNotice := setupTestReportsServer(t, comp)

	// Put schedule with Notify = true
	_, err := repStore.PutSchedule(reports.Schedule{
		Name:    "Clean Compliance Check Notifying",
		Kind:    reports.Compliance,
		Format:  reports.HTML,
		Every:   "daily",
		At:      "00:00",
		Enabled: true,
		Notify:  true,
	})
	if err != nil {
		t.Fatalf("PutSchedule: %v", err)
	}

	ctx := context.Background()
	srv.runDueReportSchedules(ctx, time.Now().Add(-48*time.Hour))

	// Notify=true -> Notice should be sent with normal title
	notice, ok := waitNotice(2 * time.Second)
	if !ok {
		t.Fatal("expected notification when Notify=true, but none received")
	}

	if strings.Contains(notice.Subject, "compliance checks failed") {
		t.Errorf("subject should NOT contain 'checks failed' for all-passing report, got %q", notice.Subject)
	}
	if !strings.Contains(notice.Message, "100% compliance score") {
		t.Errorf("expected 100%% compliance score in message, got %q", notice.Message)
	}
}
