package reports

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/compliance"
)

func facts(now time.Time) Facts {
	return Facts{
		Hosts: []HostFacts{
			{Name: "hv1", CPUs: 16, MemoryKiB: 64 << 20,
				Machines: []Machine{
					{UUID: "u-db", Name: "db", State: "running", VCPUs: 8, MemoryMiB: 16384, DiskBytes: 100 << 30, Disks: 1},
					{UUID: "u-web", Name: "web<script>", State: "shutoff", VCPUs: 2, MemoryMiB: 2048, DiskBytes: 20 << 30, Disks: 1},
					{UUID: "u-tpl", Name: "debian-tpl", State: "shutoff", VCPUs: 2, MemoryMiB: 2048, Template: true},
				},
				Pools: []Pool{
					{Name: "default", Type: "dir", Active: true, CapacityBytes: 500 << 30, AllocationBytes: 120 << 30, AvailableBytes: 380 << 30},
					{Name: "nfs", Type: "netfs", Active: true, CapacityBytes: 1 << 40, AllocationBytes: 1 << 39, AvailableBytes: 1 << 39},
				},
				Backups: []Backup{
					{DomainUUID: "u-db", Created: now.Add(-20 * time.Hour), SizeBytes: 10 << 30},
					{DomainUUID: "u-db", Created: now.Add(-44 * time.Hour), SizeBytes: 10 << 30},
				},
				Schedules: []BackupSchedule{{DomainUUID: "u-db", Interval: 24 * time.Hour}, {DomainUUID: "u-web", Interval: 24 * time.Hour}},
			},
			{Name: "hv2", CPUs: 8, MemoryKiB: 32 << 20,
				Machines:  []Machine{{UUID: "u-app", Name: "app", State: "running", VCPUs: 4, MemoryMiB: 8192}},
				Pools:     []Pool{{Name: "nfs", Type: "netfs", Active: true, CapacityBytes: 1 << 40, AllocationBytes: 1 << 39, AvailableBytes: 1 << 39}},
				Backups:   []Backup{{DomainUUID: "u-app", Created: now.Add(-100 * time.Hour)}},
				Schedules: []BackupSchedule{{DomainUUID: "u-app", Interval: 24 * time.Hour}},
			},
		},
		Missing: []string{"hv3"},
	}
}

func open(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, now
}

func TestCapacityCountsSharedPoolsOnce(t *testing.T) {
	s, now := open(t)
	m, err := s.Generate(Capacity, JSON, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	_, body, err := s.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var d CapacityData
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Total.CPUs != 24 || d.Total.VCPUs != 14 || d.Total.VCPUsRunning != 12 || d.Total.Machines != 3 || d.Total.On != 2 {
		t.Errorf("total = %+v (templates must not count)", d.Total)
	}
	if len(d.Pools) != 2 {
		t.Errorf("pools = %+v (the NFS pool of both hosts is one)", d.Pools)
	}
	if !strings.Contains(m.Summary, "3 machines (2 on)") || !strings.Contains(m.Summary, "not reachable: hv3") {
		t.Errorf("summary = %q", m.Summary)
	}
}

func TestInventoryHTMLEscapes(t *testing.T) {
	s, now := open(t)
	m, err := s.Generate(Inventory, HTML, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := s.Get(m.ID)
	if strings.Contains(string(body), "<script>") || !strings.Contains(string(body), "web&lt;script&gt;") {
		t.Error("machine name not escaped")
	}
	if !strings.Contains(m.Summary, "4 machines on 2 hosts, 2 running") {
		t.Errorf("summary = %q", m.Summary)
	}
}

func TestBackupSLA(t *testing.T) {
	s, now := open(t)
	m, err := s.Generate(BackupSLA, JSON, facts(now), "onur", "")
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := s.Get(m.ID)
	var items []SLAItem
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Machine] = it.Status
	}
	want := map[string]string{"db": "ok", "web<script>": "never", "app": "overdue"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["debian-tpl"]; ok {
		t.Error("template listed")
	}
	if slaStatus(24*time.Hour, ptr(now.Add(-50*time.Hour)), now) != "late" || slaStatus(0, nil, now) != "unscheduled" {
		t.Error("slaStatus thresholds")
	}
}

func ptr(t time.Time) *time.Time { return &t }

func TestSchedulesDueAndRetention(t *testing.T) {
	s, now := open(t)
	if _, err := s.PutSchedule(Schedule{Name: "x", Kind: "nope", Format: HTML, Every: "daily", At: "06:00"}); err == nil {
		t.Error("bad kind accepted")
	}
	sc, err := s.PutSchedule(Schedule{Name: "Weekly capacity", Kind: Capacity, Format: HTML, Every: "weekly", At: "06:00", Enabled: true, KeepDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-16 is a Friday: the next Monday 06:00 is the 19th.
	if n := sc.Next(now); n.Day() != 19 || n.Hour() != 6 || n.Weekday() != time.Monday {
		t.Errorf("next = %v", n)
	}
	if len(s.Due(now.Add(-time.Hour))) != 0 {
		t.Error("due before its time")
	}
	if due := s.Due(now.AddDate(0, 0, -10)); len(due) != 1 {
		t.Fatalf("not due after it was missed: %v", due)
	}
	// An old report of this schedule is deleted after the next run, a manual one stays.
	old, _ := s.Generate(Capacity, HTML, facts(now), "schedule: x", sc.ID)
	manual, _ := s.Generate(Capacity, HTML, facts(now), "onur", "")
	later := now.AddDate(0, 0, 40)
	s.now = func() time.Time { return later }
	s.Ran(sc.ID, nil)
	if _, _, err := s.Get(old.ID); err == nil {
		t.Error("expired scheduled report kept")
	}
	if _, _, err := s.Get(manual.ID); err != nil {
		t.Error("manual report deleted by retention")
	}
	if len(s.Due(now)) != 0 {
		t.Error("due again right after running")
	}
	if n := (Schedule{Every: "monthly", At: "01:00"}).Next(now); n.Day() != 1 || n.Month() != time.November {
		t.Errorf("monthly next = %v", n)
	}
}

func TestComplianceScheduleIntegration(t *testing.T) {
	s, now := open(t)

	// Create a report schedule with kind="compliance"
	sc, err := s.PutSchedule(Schedule{
		Name:     "Nightly security scan",
		Kind:     Compliance,
		Format:   HTML,
		Every:    "daily",
		At:       "02:00",
		Enabled:  true,
		KeepDays: 14,
	})
	if err != nil {
		t.Fatalf("failed to create compliance schedule: %v", err)
	}

	if sc.Kind != Compliance {
		t.Errorf("schedule Kind = %q, want %q", sc.Kind, Compliance)
	}

	// Create test facts with compliance information
	f := Facts{
		Compliance: []compliance.HostCompliance{
			{
				HostID:       "host-alpha",
				HostName:     "alpha.cluster.local",
				EvaluatedAt:  now,
				Score:        100,
				PassedCount:  5,
				FailedCount:  0,
				WarningCount: 0,
				TotalCount:   5,
				Results: []compliance.CheckResult{
					{
						ID:          "ssh-root-login",
						Title:       "Disable SSH Root Login",
						Category:    "SSH",
						Severity:    compliance.SeverityCritical,
						Status:      compliance.StatusPass,
						Detail:      "PermitRootLogin is prohibit-password",
						Remediation: "Set PermitRootLogin prohibit-password",
						Benchmark:   "CIS Linux 5.2.5",
					},
				},
			},
			{
				HostID:       "host-beta",
				HostName:     "beta.cluster.local",
				EvaluatedAt:  now,
				Score:        50,
				PassedCount:  1,
				FailedCount:  1,
				WarningCount: 0,
				TotalCount:   2,
				Results: []compliance.CheckResult{
					{
						ID:          "host-firewall",
						Title:       "Host Firewall Enabled",
						Category:    "Firewall",
						Severity:    compliance.SeverityHigh,
						Status:      compliance.StatusFail,
						Detail:      "No active ruleset",
						Remediation: "systemctl enable --now nftables",
						Benchmark:   "CIS Linux 3.5.1",
					},
					{
						ID:          "kernel-kptr",
						Title:       "Restrict Kernel Pointer Leaks",
						Category:    "Kernel",
						Severity:    compliance.SeverityMedium,
						Status:      compliance.StatusPass,
						Detail:      "kernel.kptr_restrict = 1",
						Remediation: "sysctl -w kernel.kptr_restrict=1",
						Benchmark:   "CIS Linux 1.5.1",
					},
				},
			},
		},
	}

	// Generate report through reports.Store
	m, err := s.Generate(Compliance, HTML, f, "schedule: "+sc.Name, sc.ID)
	if err != nil {
		t.Fatalf("Generate(Compliance, HTML) failed: %v", err)
	}

	// Validate stored metadata
	if m.Kind != Compliance {
		t.Errorf("m.Kind = %q, want %q", m.Kind, Compliance)
	}
	if m.Format != HTML {
		t.Errorf("m.Format = %q, want %q", m.Format, HTML)
	}
	if m.ScheduleID != sc.ID {
		t.Errorf("m.ScheduleID = %q, want %q", m.ScheduleID, sc.ID)
	}
	if m.By != "schedule: "+sc.Name {
		t.Errorf("m.By = %q, want %q", m.By, "schedule: "+sc.Name)
	}
	// Total checks: 5 + 2 = 7; passed: 5 + 1 = 6 -> (6 * 100) / 7 = 85%
	if !strings.Contains(m.Summary, "2 hosts · 85% compliance score · 7 checks: 6 passed, 1 failed, 0 warnings") {
		t.Errorf("m.Summary = %q", m.Summary)
	}

	// Validate report retrieval from Store
	gotMeta, body, err := s.Get(m.ID)
	if err != nil {
		t.Fatalf("s.Get(%q) failed: %v", m.ID, err)
	}
	if gotMeta.ID != m.ID {
		t.Errorf("gotMeta.ID = %q, want %q", gotMeta.ID, m.ID)
	}
	if len(body) == 0 {
		t.Fatal("empty report body returned by s.Get")
	}

	// Validate file on disk
	expectedFile := filepath.Join(s.dir, m.ID+".html")
	diskBytes, err := os.ReadFile(expectedFile)
	if err != nil {
		t.Fatalf("failed to read report file from disk: %v", err)
	}
	if !bytes.Equal(diskBytes, body) {
		t.Error("file on disk does not match body returned by Store.Get")
	}

	// Check HTML content
	if !strings.Contains(string(diskBytes), "Host Hardening &amp; Security Compliance Report") {
		t.Error("report HTML missing header title")
	}
	if !strings.Contains(string(diskBytes), "alpha.cluster.local") {
		t.Error("host alpha not found in HTML report")
	}
	if !strings.Contains(string(diskBytes), "beta.cluster.local") {
		t.Error("host beta not found in HTML report")
	}

	// Also verify CSV and JSON formats can be generated through Store
	mCSV, err := s.Generate(Compliance, CSV, f, "admin", "")
	if err != nil {
		t.Fatalf("Generate(Compliance, CSV) failed: %v", err)
	}
	if _, csvBody, err := s.Get(mCSV.ID); err != nil || len(csvBody) == 0 {
		t.Fatalf("failed to get CSV report: %v", err)
	}

	mJSON, err := s.Generate(Compliance, JSON, f, "admin", "")
	if err != nil {
		t.Fatalf("Generate(Compliance, JSON) failed: %v", err)
	}
	if _, jsonBody, err := s.Get(mJSON.ID); err != nil || len(jsonBody) == 0 {
		t.Fatalf("failed to get JSON report: %v", err)
	}
}

func TestShowbackReportAndValidation(t *testing.T) {
	s, now := open(t)

	// Validate schedule accepts both Showback and Compliance
	for _, k := range []Kind{Capacity, Inventory, BackupSLA, Showback, Compliance} {
		sc, err := s.PutSchedule(Schedule{
			Name:    string(k) + " schedule",
			Kind:    k,
			Format:  JSON,
			Every:   "daily",
			At:      "03:00",
			Enabled: true,
		})
		if err != nil {
			t.Fatalf("PutSchedule failed for kind %s: %v", k, err)
		}
		if sc.Kind != k {
			t.Errorf("Kind = %s, want %s", sc.Kind, k)
		}
	}

	// Without builder, showback fails cleanly
	_, err := s.Generate(Showback, JSON, facts(now), "admin", "")
	if err == nil || !strings.Contains(err.Error(), "showback reports are not configured") {
		t.Errorf("expected not configured error, got %v", err)
	}

	// With builder registered, showback generates correctly
	s.SetShowbackBuilder(func(f Format) (string, []byte, error) {
		return "1 project · total cost $42.00", []byte(`{"period":"2026-10","totalCost":42}`), nil
	})
	m, err := s.Generate(Showback, JSON, facts(now), "admin", "")
	if err != nil {
		t.Fatalf("Generate(Showback) failed: %v", err)
	}
	if m.Kind != Showback || !strings.Contains(m.Summary, "total cost $42.00") {
		t.Errorf("unexpected metadata: %+v", m)
	}
	_, body, err := s.Get(m.ID)
	if err != nil || !strings.Contains(string(body), "totalCost") {
		t.Errorf("s.Get failed or wrong body: %v, %s", err, string(body))
	}
}


