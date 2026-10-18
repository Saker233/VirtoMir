package drs

import (
	"context"
	"testing"
)

func TestDRS_BalancedCluster(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 40.0, MemPct: 50.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 42.0, MemPct: 48.0},
	}

	status := eng.Evaluate(context.Background(), hosts, nil)
	if status.BalanceScore < 90.0 {
		t.Errorf("expected high balance score, got %.1f", status.BalanceScore)
	}
	if len(status.Recommendations) != 0 {
		t.Errorf("expected no recommendations on balanced cluster, got %d", len(status.Recommendations))
	}
}

func TestDRS_UnbalancedClusterGeneratesRecommendation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 85.0, MemPct: 80.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 10.0, MemPct: 15.0},
	}

	domains := []DomainPlacement{
		{UUID: "vm-1", Name: "heavy-app", HostID: "host-1", State: "running", CPUPct: 35.0, MemUsedB: 8 * 1024 * 1024 * 1024},
		{UUID: "vm-2", Name: "light-app", HostID: "host-1", State: "running", CPUPct: 10.0, MemUsedB: 2 * 1024 * 1024 * 1024},
	}

	status := eng.Evaluate(context.Background(), hosts, domains)
	if status.BalanceScore > 50.0 {
		t.Errorf("expected low balance score on skewed cluster, got %.1f", status.BalanceScore)
	}
	if len(status.Recommendations) == 0 {
		t.Fatal("expected at least one recommendation for skewed cluster")
	}

	rec := status.Recommendations[0]
	if rec.SourceHostID != "host-1" || rec.TargetHostID != "host-2" {
		t.Errorf("expected migration from host-1 to host-2, got %s -> %s", rec.SourceHostID, rec.TargetHostID)
	}
	if rec.DomainUUID != "vm-1" {
		t.Errorf("expected heavy-app (vm-1) migration, got %s", rec.DomainUUID)
	}
	// Applying re-evaluates and looks the recommendation up by ID.
	if again := eng.Evaluate(context.Background(), hosts, domains); again.Recommendations[0].ID != rec.ID {
		t.Errorf("recommendation ID changed between evaluations: %s, then %s", rec.ID, again.Recommendations[0].ID)
	}
}

func TestDRS_AntiAffinityRuleViolation(t *testing.T) {
	dir := t.TempDir()
	eng, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rule, err := eng.AddRule(AffinityRule{
		Name:      "DNS Redundancy",
		Kind:      AntiAffinityVMVM,
		Mandatory: true,
		Domains:   []string{"dns-01", "dns-02"},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	hosts := []HostMetrics{
		{ID: "host-1", Name: "hv-01", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
		{ID: "host-2", Name: "hv-02", Reachable: true, CPUPct: 20.0, MemPct: 20.0},
	}

	// Both VMs are on host-1!
	domains := []DomainPlacement{
		{UUID: "dns-01", Name: "dns-01", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
		{UUID: "dns-02", Name: "dns-02", HostID: "host-1", State: "running", CPUPct: 5.0, MemUsedB: 1024 * 1024 * 1024},
	}

	status := eng.Evaluate(context.Background(), hosts, domains)
	if len(status.Recommendations) == 0 {
		t.Fatal("expected recommendation to resolve anti-affinity violation")
	}

	rec := status.Recommendations[0]
	if rec.Priority != 5 {
		t.Errorf("expected priority 5 for rule violation, got %d", rec.Priority)
	}
	if rec.DomainUUID != "dns-02" {
		t.Errorf("expected second VM to move, got %s", rec.DomainUUID)
	}
	if rec.TargetHostID != "host-2" {
		t.Errorf("expected move to host-2, got %s", rec.TargetHostID)
	}

	// Test deleting rule
	if err := eng.DeleteRule(rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if len(eng.GetConfig().Rules) != 0 {
		t.Errorf("expected 0 rules after deletion")
	}
}

func TestPlacementPicksTheLeastLoadedHostWithRoom(t *testing.T) {
	const gib = 1 << 30
	hosts := []HostMetrics{
		{ID: "busy", Reachable: true, CPUPct: 70, MemPct: 60, MemUsedB: 60 * gib, MemTotalB: 100 * gib},
		{ID: "idle-but-full", Reachable: true, CPUPct: 5, MemPct: 98, MemUsedB: 98 * gib, MemTotalB: 100 * gib},
		{ID: "quiet", Reachable: true, CPUPct: 20, MemPct: 30, MemUsedB: 30 * gib, MemTotalB: 100 * gib},
		{ID: "down", CPUPct: 0, MemPct: 0, MemTotalB: 100 * gib},
	}
	if h, ok := Placement(hosts, 8*gib); !ok || h.ID != "quiet" {
		t.Errorf("placement = %s, %v", h.ID, ok)
	}
	if _, ok := Placement(hosts, 80*gib); ok {
		t.Error("placed a machine no host has room for")
	}
}
