package catalog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open non-existent file failed: %v", err)
	}

	p := s.GetPricing()
	if p.VCPUHour != 0.05 || p.RAMGiBHour != 0.01 || p.DiskGiBMonth != 0.10 || p.Currency != "USD" {
		t.Errorf("unexpected default pricing: %+v", p)
	}

	if bps := s.ListBlueprints(""); len(bps) != 0 {
		t.Errorf("expected 0 blueprints, got %d", len(bps))
	}
	if reqs := s.ListRequests("", nil, ""); len(reqs) != 0 {
		t.Errorf("expected 0 requests, got %d", len(reqs))
	}
	if leases := s.ListActiveLeases(); len(leases) != 0 {
		t.Errorf("expected 0 active leases, got %d", len(leases))
	}
}

func TestStore_BlueprintValidation(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open memory store failed: %v", err)
	}

	validPreset := SizePreset{ID: "small", Name: "Small", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20}

	tests := []struct {
		name string
		bp   Blueprint
	}{
		{
			name: "empty id",
			bp:   Blueprint{ID: "", Name: "Valid", SourceType: SourceTypeTemplate, Presets: []SizePreset{validPreset}},
		},
		{
			name: "uppercase slug",
			bp:   Blueprint{ID: "INVALID", Name: "Valid", SourceType: SourceTypeTemplate, Presets: []SizePreset{validPreset}},
		},
		{
			name: "leading dash",
			bp:   Blueprint{ID: "-invalid", Name: "Valid", SourceType: SourceTypeTemplate, Presets: []SizePreset{validPreset}},
		},
		{
			name: "trailing dash",
			bp:   Blueprint{ID: "invalid-", Name: "Valid", SourceType: SourceTypeTemplate, Presets: []SizePreset{validPreset}},
		},
		{
			name: "too short slug",
			bp:   Blueprint{ID: "ab", Name: "Valid", SourceType: SourceTypeTemplate, Presets: []SizePreset{validPreset}},
		},
		{
			name: "empty name",
			bp:   Blueprint{ID: "valid-bp", Name: "   ", SourceType: SourceTypeTemplate, Presets: []SizePreset{validPreset}},
		},
		{
			name: "invalid source type",
			bp:   Blueprint{ID: "valid-bp", Name: "Valid", SourceType: "docker", Presets: []SizePreset{validPreset}},
		},
		{
			name: "missing presets",
			bp:   Blueprint{ID: "valid-bp", Name: "Valid", SourceType: SourceTypeTemplate, Presets: nil},
		},
		{
			name: "invalid default preset ID",
			bp: Blueprint{
				ID:              "valid-bp",
				Name:            "Valid",
				SourceType:      SourceTypeTemplate,
				Presets:         []SizePreset{validPreset},
				DefaultPresetID: "non-existent-preset",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateBlueprint(tc.bp)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got: %v", err)
			}
		})
	}

	// Valid blueprint creation
	bp := Blueprint{
		ID:              "ubuntu-server",
		Name:            "Ubuntu Server 24.04",
		SourceType:      SourceTypeImage,
		SourceRef:       "ubuntu-24.04-cloudimg",
		Presets:         []SizePreset{validPreset},
		DefaultPresetID: "small",
	}
	created, err := s.CreateBlueprint(bp)
	if err != nil {
		t.Fatalf("CreateBlueprint failed: %v", err)
	}
	if created.ID != bp.ID || created.Name != bp.Name {
		t.Errorf("created blueprint mismatch: %+v", created)
	}
	if created.Created.IsZero() || created.Updated.IsZero() {
		t.Errorf("expected Created/Updated timestamps to be set")
	}

	// Duplicate ID must return ErrConflict
	_, err = s.CreateBlueprint(bp)
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on duplicate ID, got: %v", err)
	}
}

func TestStore_BlueprintFilteringAndCRUD(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	preset := SizePreset{ID: "standard", Name: "Standard", VCPUs: 4, MemoryMiB: 8192, DiskGiB: 50}

	bp1 := Blueprint{
		ID:              "shared-bp",
		Name:            "B Shared Blueprint",
		SourceType:      SourceTypeTemplate,
		Presets:         []SizePreset{preset},
		AllowedProjects: []string{}, // available to all
	}
	bp2 := Blueprint{
		ID:              "proj-a-bp",
		Name:            "A Project A Blueprint",
		SourceType:      SourceTypeImage,
		Presets:         []SizePreset{preset},
		AllowedProjects: []string{"proj-a"},
	}
	bp3 := Blueprint{
		ID:              "proj-b-bp",
		Name:            "C Project B Blueprint",
		SourceType:      SourceTypeImage,
		Presets:         []SizePreset{preset},
		AllowedProjects: []string{"proj-b"},
	}

	for _, b := range []Blueprint{bp1, bp2, bp3} {
		if _, err := s.CreateBlueprint(b); err != nil {
			t.Fatalf("CreateBlueprint %s failed: %v", b.ID, err)
		}
	}

	// List all sorted by name: bp2 ("A..."), bp1 ("B..."), bp3 ("C...")
	all := s.ListBlueprints("")
	if len(all) != 3 {
		t.Fatalf("expected 3 blueprints, got %d", len(all))
	}
	if all[0].ID != "proj-a-bp" || all[1].ID != "shared-bp" || all[2].ID != "proj-b-bp" {
		t.Errorf("unexpected ordering: %v, %v, %v", all[0].ID, all[1].ID, all[2].ID)
	}

	// Filter by project "proj-a": shared-bp and proj-a-bp
	projA := s.ListBlueprints("proj-a")
	if len(projA) != 2 {
		t.Fatalf("expected 2 blueprints for proj-a, got %d", len(projA))
	}
	if projA[0].ID != "proj-a-bp" || projA[1].ID != "shared-bp" {
		t.Errorf("unexpected proj-a blueprints: %+v", projA)
	}

	// Filter by project "proj-b": shared-bp and proj-b-bp
	projB := s.ListBlueprints("proj-b")
	if len(projB) != 2 {
		t.Fatalf("expected 2 blueprints for proj-b, got %d", len(projB))
	}

	// Filter by project "proj-other": shared-bp only
	projOther := s.ListBlueprints("proj-other")
	if len(projOther) != 1 || projOther[0].ID != "shared-bp" {
		t.Fatalf("expected 1 blueprint for proj-other, got %+v", projOther)
	}

	// GetBlueprint
	got, ok := s.GetBlueprint("proj-a-bp")
	if !ok || got.Name != "A Project A Blueprint" {
		t.Errorf("GetBlueprint failed: ok=%v, got=%+v", ok, got)
	}
	_, ok = s.GetBlueprint("non-existent")
	if ok {
		t.Errorf("GetBlueprint expected false for non-existent")
	}

	// UpdateBlueprint
	got.Description = "Updated Description"
	got.RequireApproval = true
	if err := s.UpdateBlueprint(got); err != nil {
		t.Fatalf("UpdateBlueprint failed: %v", err)
	}
	updated, _ := s.GetBlueprint(got.ID)
	if updated.Description != "Updated Description" || !updated.RequireApproval {
		t.Errorf("updated blueprint fields mismatch: %+v", updated)
	}

	// Update non-existent returns ErrNotFound
	nonExistent := got
	nonExistent.ID = "ghost-bp"
	if err := s.UpdateBlueprint(nonExistent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on update non-existent, got: %v", err)
	}

	// DeleteBlueprint
	if err := s.DeleteBlueprint("proj-b-bp"); err != nil {
		t.Fatalf("DeleteBlueprint failed: %v", err)
	}
	if _, ok := s.GetBlueprint("proj-b-bp"); ok {
		t.Errorf("expected deleted blueprint to be gone")
	}
	if err := s.DeleteBlueprint("proj-b-bp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on deleting missing blueprint, got: %v", err)
	}
}

func TestStore_PersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open s1 failed: %v", err)
	}

	// Create blueprint
	bp := Blueprint{
		ID:         "db-cluster",
		Name:       "Database Cluster",
		SourceType: SourceTypeTemplate,
		Presets: []SizePreset{
			{ID: "db-large", Name: "Large", VCPUs: 8, MemoryMiB: 16384, DiskGiB: 200},
		},
		DefaultPresetID: "db-large",
	}
	if _, err := s1.CreateBlueprint(bp); err != nil {
		t.Fatalf("CreateBlueprint failed: %v", err)
	}

	// Create request
	req := Request{
		ID:          "req-persist-1",
		BlueprintID: "db-cluster",
		ProjectID:   "finance",
		Requester:   "bob",
		VMName:      "fin-db01",
		PresetID:    "db-large",
		VCPUs:       8,
		MemoryMiB:   16384,
		DiskGiB:     200,
		Status:      StatusApproved,
	}
	if _, err := s1.CreateRequest(req); err != nil {
		t.Fatalf("CreateRequest failed: %v", err)
	}

	// Save lease
	lease := Lease{
		RequestID:   "req-persist-1",
		MachineUUID: "machine-uuid-1234",
		ProjectID:   "finance",
		Expiry:      time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second),
		State:       LeaseStateActive,
	}
	if err := s1.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease failed: %v", err)
	}

	// Update pricing
	pricing := PricingConfig{
		VCPUHour:     0.075,
		RAMGiBHour:   0.015,
		DiskGiBMonth: 0.12,
		Currency:     "EUR",
	}
	if err := s1.SetPricing(pricing); err != nil {
		t.Fatalf("SetPricing failed: %v", err)
	}

	// Reopen store from disk
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open s2 failed: %v", err)
	}

	gotBP, ok := s2.GetBlueprint("db-cluster")
	if !ok || gotBP.Name != bp.Name || len(gotBP.Presets) != 1 {
		t.Errorf("persisted blueprint mismatch: ok=%v, got=%+v", ok, gotBP)
	}

	gotReq, ok := s2.GetRequest("req-persist-1")
	if !ok || gotReq.VMName != "fin-db01" || gotReq.Status != StatusApproved {
		t.Errorf("persisted request mismatch: ok=%v, got=%+v", ok, gotReq)
	}

	gotLease, ok := s2.GetLease("machine-uuid-1234")
	if !ok || gotLease.RequestID != "req-persist-1" || gotLease.State != LeaseStateActive {
		t.Errorf("persisted lease mismatch: ok=%v, got=%+v", ok, gotLease)
	}

	gotPricing := s2.GetPricing()
	if gotPricing.VCPUHour != 0.075 || gotPricing.Currency != "EUR" {
		t.Errorf("persisted pricing mismatch: %+v", gotPricing)
	}
}

func TestStore_RequestLifecycleAndFiltering(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Validation in CreateRequest
	badReqs := []struct {
		name string
		req  Request
	}{
		{"missing blueprint", Request{ProjectID: "p1", Requester: "u1", VMName: "vm1"}},
		{"missing project", Request{BlueprintID: "bp1", Requester: "u1", VMName: "vm1"}},
		{"missing requester", Request{BlueprintID: "bp1", ProjectID: "p1", VMName: "vm1"}},
		{"missing vmname", Request{BlueprintID: "bp1", ProjectID: "p1", Requester: "u1"}},
	}
	for _, tc := range badReqs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateRequest(tc.req)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got: %v", err)
			}
		})
	}

	now := time.Now().UTC()

	req1 := Request{
		ID:          "req-1",
		BlueprintID: "web-bp",
		ProjectID:   "frontend",
		Requester:   "alice",
		VMName:      "web-dev-1",
		Status:      StatusPendingApproval,
		CreatedAt:   now.Add(-2 * time.Hour),
	}
	req2 := Request{
		ID:          "req-2",
		BlueprintID: "web-bp",
		ProjectID:   "frontend",
		Requester:   "bob",
		VMName:      "web-dev-2",
		Status:      StatusApproved,
		CreatedAt:   now.Add(-1 * time.Hour),
	}
	req3 := Request{
		ID:          "req-3",
		BlueprintID: "db-bp",
		ProjectID:   "backend",
		Requester:   "alice",
		VMName:      "api-db",
		Status:      StatusActive,
		CreatedAt:   now,
	}

	for _, r := range []Request{req1, req2, req3} {
		if _, err := s.CreateRequest(r); err != nil {
			t.Fatalf("CreateRequest failed: %v", err)
		}
	}

	// Auto ID generation when empty ID
	autoReq := Request{
		BlueprintID: "web-bp",
		ProjectID:   "infra",
		Requester:   "charlie",
		VMName:      "test-vm",
	}
	createdAuto, err := s.CreateRequest(autoReq)
	if err != nil {
		t.Fatalf("CreateRequest auto ID failed: %v", err)
	}
	if len(createdAuto.ID) != 16 {
		t.Errorf("expected 16-hex ID, got %q", createdAuto.ID)
	}
	if createdAuto.Status != StatusPendingApproval {
		t.Errorf("expected default status %q, got %q", StatusPendingApproval, createdAuto.Status)
	}

	// Filter by user "alice": returns req3, then req1 (newest first)
	aliceReqs := s.ListRequests("alice", nil, "")
	if len(aliceReqs) != 2 {
		t.Fatalf("expected 2 requests for alice, got %d", len(aliceReqs))
	}
	if aliceReqs[0].ID != "req-3" || aliceReqs[1].ID != "req-1" {
		t.Errorf("unexpected alice requests ordering: %s, %s", aliceReqs[0].ID, aliceReqs[1].ID)
	}

	// Filter by projects ["frontend"]
	frontendReqs := s.ListRequests("", []string{"frontend"}, "")
	if len(frontendReqs) != 2 {
		t.Fatalf("expected 2 frontend requests, got %d", len(frontendReqs))
	}
	if frontendReqs[0].ID != "req-2" || frontendReqs[1].ID != "req-1" {
		t.Errorf("unexpected frontend requests ordering: %s, %s", frontendReqs[0].ID, frontendReqs[1].ID)
	}

	// Filter by status StatusActive
	activeReqs := s.ListRequests("", nil, StatusActive)
	if len(activeReqs) != 1 || activeReqs[0].ID != "req-3" {
		t.Fatalf("expected req-3 for active status, got: %+v", activeReqs)
	}

	// UpdateRequest
	req1.Status = StatusRejected
	reviewComment := "Budget exceeded"
	req1.ReviewComment = reviewComment
	if err := s.UpdateRequest(req1); err != nil {
		t.Fatalf("UpdateRequest failed: %v", err)
	}
	updatedReq, ok := s.GetRequest("req-1")
	if !ok || updatedReq.Status != StatusRejected || updatedReq.ReviewComment != reviewComment {
		t.Errorf("updated request mismatch: ok=%v, got=%+v", ok, updatedReq)
	}

	// Update non-existent
	ghostReq := req1
	ghostReq.ID = "ghost-request"
	if err := s.UpdateRequest(ghostReq); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on updating missing request, got: %v", err)
	}

	// DeleteRequest
	if err := s.DeleteRequest("req-2"); err != nil {
		t.Fatalf("DeleteRequest failed: %v", err)
	}
	if _, ok := s.GetRequest("req-2"); ok {
		t.Errorf("expected deleted request to be missing")
	}
	if err := s.DeleteRequest("req-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on deleting missing request, got: %v", err)
	}
}

func TestStore_LeaseRetrievalAndLifecycle(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Validation
	if err := s.SaveLease(Lease{MachineUUID: "", RequestID: "r1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty MachineUUID, got: %v", err)
	}
	if err := s.SaveLease(Lease{MachineUUID: "m1", RequestID: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty RequestID, got: %v", err)
	}

	now := time.Now().UTC()

	l1 := Lease{
		RequestID:   "req-1",
		MachineUUID: "uuid-1",
		ProjectID:   "p1",
		Expiry:      now.Add(24 * time.Hour),
		State:       LeaseStateActive,
	}
	l2 := Lease{
		RequestID:   "req-2",
		MachineUUID: "uuid-2",
		ProjectID:   "p1",
		Expiry:      now.Add(6 * time.Hour),
		State:       LeaseStateReminderSent,
	}
	l3 := Lease{
		RequestID:   "req-3",
		MachineUUID: "uuid-3",
		ProjectID:   "p2",
		Expiry:      now.Add(-2 * time.Hour),
		State:       LeaseStateTerminated,
	}

	for _, l := range []Lease{l1, l2, l3} {
		if err := s.SaveLease(l); err != nil {
			t.Fatalf("SaveLease failed: %v", err)
		}
	}

	// GetLease by machine UUID
	got1, ok := s.GetLease("uuid-1")
	if !ok || got1.RequestID != "req-1" {
		t.Errorf("GetLease(uuid-1) failed: ok=%v, got=%+v", ok, got1)
	}
	_, ok = s.GetLease("missing-uuid")
	if ok {
		t.Errorf("GetLease(missing-uuid) expected false")
	}

	// GetLeaseByRequest
	got2, ok := s.GetLeaseByRequest("req-2")
	if !ok || got2.MachineUUID != "uuid-2" {
		t.Errorf("GetLeaseByRequest(req-2) failed: ok=%v, got=%+v", ok, got2)
	}
	_, ok = s.GetLeaseByRequest("missing-request")
	if ok {
		t.Errorf("GetLeaseByRequest(missing-request) expected false")
	}

	// ListActiveLeases: should return l2 then l1 (ordered by expiry, excluding terminated l3)
	active := s.ListActiveLeases()
	if len(active) != 2 {
		t.Fatalf("expected 2 active leases, got %d", len(active))
	}
	if active[0].MachineUUID != "uuid-2" || active[1].MachineUUID != "uuid-1" {
		t.Errorf("unexpected active leases ordering: %v, %v", active[0].MachineUUID, active[1].MachineUUID)
	}

	// DeleteLease
	if err := s.DeleteLease("uuid-1"); err != nil {
		t.Fatalf("DeleteLease failed: %v", err)
	}
	if _, ok := s.GetLease("uuid-1"); ok {
		t.Errorf("expected deleted lease to be gone")
	}
	if err := s.DeleteLease("uuid-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on deleting missing lease, got: %v", err)
	}
}

func TestStore_PricingConfig(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Validate non-negative rates
	badConfigs := []PricingConfig{
		{VCPUHour: -0.01, RAMGiBHour: 0.01, DiskGiBMonth: 0.10, Currency: "USD"},
		{VCPUHour: 0.05, RAMGiBHour: -0.01, DiskGiBMonth: 0.10, Currency: "USD"},
		{VCPUHour: 0.05, RAMGiBHour: 0.01, DiskGiBMonth: -0.10, Currency: "USD"},
	}

	for _, cfg := range badConfigs {
		if err := s.SetPricing(cfg); err == nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected ErrInvalid for negative rate %+v, got: %v", cfg, err)
		}
	}

	validCfg := PricingConfig{
		VCPUHour:     0.10,
		RAMGiBHour:   0.02,
		DiskGiBMonth: 0.20,
		Currency:     "GBP",
	}
	if err := s.SetPricing(validCfg); err != nil {
		t.Fatalf("SetPricing failed: %v", err)
	}

	got := s.GetPricing()
	if got != validCfg {
		t.Errorf("pricing mismatch: expected %+v, got %+v", validCfg, got)
	}

	// Empty currency fallback to USD
	noCurrency := PricingConfig{VCPUHour: 0.05, RAMGiBHour: 0.01, DiskGiBMonth: 0.10}
	if err := s.SetPricing(noCurrency); err != nil {
		t.Fatalf("SetPricing failed: %v", err)
	}
	if s.GetPricing().Currency != "USD" {
		t.Errorf("expected USD fallback currency, got %q", s.GetPricing().Currency)
	}
}
