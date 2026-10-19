package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/project"
)

type mockAlertNotifier struct {
	mu      sync.Mutex
	notices []struct {
		domain  string
		title   string
		message string
	}
}

func (m *mockAlertNotifier) Notice(domain, title, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notices = append(m.notices, struct {
		domain  string
		title   string
		message string
	}{domain: domain, title: title, message: message})
}

func (m *mockAlertNotifier) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notices)
}

func (m *mockAlertNotifier) last() (domain, title, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.notices) == 0 {
		return "", "", ""
	}
	last := m.notices[len(m.notices)-1]
	return last.domain, last.title, last.message
}

type mockDomainController struct {
	mu            sync.Mutex
	shutdowns     []string
	destroys      []string
	deletes       []string
	shutdownErr   error
	destroyErr    error
	deleteErr     error
	shutdownDelay time.Duration
}

func (m *mockDomainController) ShutdownDomain(uuid string) error {
	m.mu.Lock()
	m.shutdowns = append(m.shutdowns, uuid)
	delay := m.shutdownDelay
	err := m.shutdownErr
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	return err
}

func (m *mockDomainController) DestroyDomain(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.destroys = append(m.destroys, uuid)
	return m.destroyErr
}

func (m *mockDomainController) DeleteDomain(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, uuid)
	return m.deleteErr
}

func setupTestLeaseStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	return store
}

func setupTestProjectStore(t *testing.T) *project.Store {
	t.Helper()
	dir := t.TempDir()
	ps, err := project.Open(filepath.Join(dir, "projects.json"))
	if err != nil {
		t.Fatalf("Open project store: %v", err)
	}
	_, err = ps.Create(project.Project{
		ID:   "proj-finance",
		Name: "Finance Project",
		Members: []project.Member{
			{Kind: "user", Name: "bob", Role: "operator"},
			{Kind: "user", Name: "carol", Role: "admin"},
			{Kind: "user", Name: "dave", Role: "viewer"},
		},
	})
	if err != nil {
		t.Fatalf("Create test project: %v", err)
	}
	return ps
}

// 1. Reminder notification is sent exactly once when lease enters the reminder window.
func TestLeaseManager_ReminderSentOnce(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	// Lease expires in 23 hours. Reminder window is 24 hours.
	expiry := now.Add(23 * time.Hour)

	lease := Lease{
		RequestID:       "req-rem-1",
		MachineUUID:     "vm-uuid-rem-1",
		ProjectID:       "proj-1",
		Expiry:          expiry,
		State:           LeaseStateActive,
		ReminderSent:    false,
		LastStateChange: now.Add(-1 * time.Hour),
	}
	if err := store.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	mgr := NewLeaseManager(store, ctrl, notifier, nil,
		WithReminderBefore(24*time.Hour),
		WithGracePeriod(24*time.Hour),
	)

	// First sweep: enters reminder window.
	events := mgr.Sweep(context.Background(), now)
	if len(events) != 1 {
		t.Fatalf("expected 1 event on first sweep, got %d", len(events))
	}
	if events[0].Type != "reminder" {
		t.Errorf("expected event type 'reminder', got %q", events[0].Type)
	}
	if notifier.count() != 1 {
		t.Fatalf("expected 1 notification sent, got %d", notifier.count())
	}
	dom, title, msg := notifier.last()
	if dom != "vm-uuid-rem-1" {
		t.Errorf("expected domain %q, got %q", "vm-uuid-rem-1", dom)
	}
	if title != "Machine Lease Expiring Soon" {
		t.Errorf("expected title 'Machine Lease Expiring Soon', got %q", title)
	}
	if msg == "" {
		t.Error("expected non-empty notification message")
	}

	// Verify lease in store was updated.
	saved, ok := store.GetLease("vm-uuid-rem-1")
	if !ok {
		t.Fatal("lease not found in store")
	}
	if !saved.ReminderSent {
		t.Error("expected ReminderSent = true")
	}
	if saved.State != LeaseStateReminderSent {
		t.Errorf("expected State %q, got %q", LeaseStateReminderSent, saved.State)
	}

	// Second sweep 1 hour later (expiry still 22 hours away).
	secondNow := now.Add(1 * time.Hour)
	secondEvents := mgr.Sweep(context.Background(), secondNow)
	if len(secondEvents) != 0 {
		t.Errorf("expected 0 events on second sweep, got %d", len(secondEvents))
	}
	if notifier.count() != 1 {
		t.Errorf("expected notification count to remain 1, got %d", notifier.count())
	}
}

// 2. Expired lease triggers domain shutdown and enters grace_period with GraceExpiry set.
func TestLeaseManager_ExpiredShutdownGracePeriod(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	// Expired 10 minutes ago.
	expiry := now.Add(-10 * time.Minute)

	lease := Lease{
		RequestID:       "req-exp-1",
		MachineUUID:     "vm-uuid-exp-1",
		ProjectID:       "proj-1",
		Expiry:          expiry,
		State:           LeaseStateReminderSent,
		ReminderSent:    true,
		LastStateChange: now.Add(-24 * time.Hour),
	}
	if err := store.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	graceDuration := 12 * time.Hour
	mgr := NewLeaseManager(store, ctrl, notifier, nil,
		WithGracePeriod(graceDuration),
	)

	events := mgr.Sweep(context.Background(), now)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "shutdown" {
		t.Errorf("expected event type 'shutdown', got %q", events[0].Type)
	}

	// Verify ShutdownDomain was invoked.
	if len(ctrl.shutdowns) != 1 || ctrl.shutdowns[0] != "vm-uuid-exp-1" {
		t.Errorf("expected ShutdownDomain on 'vm-uuid-exp-1', got %v", ctrl.shutdowns)
	}
	if len(ctrl.destroys) != 0 {
		t.Errorf("expected no DestroyDomain calls when shutdown succeeds, got %v", ctrl.destroys)
	}

	// Verify notification sent.
	if notifier.count() != 1 {
		t.Fatalf("expected 1 notification sent, got %d", notifier.count())
	}
	dom, title, _ := notifier.last()
	if dom != "vm-uuid-exp-1" {
		t.Errorf("expected domain %q, got %q", "vm-uuid-exp-1", dom)
	}
	if title != "Machine Lease Expired - Powered Off" {
		t.Errorf("expected title 'Machine Lease Expired - Powered Off', got %q", title)
	}

	// Verify lease state in store.
	saved, ok := store.GetLease("vm-uuid-exp-1")
	if !ok {
		t.Fatal("lease not found in store")
	}
	if saved.State != LeaseStateGracePeriod {
		t.Errorf("expected State %q, got %q", LeaseStateGracePeriod, saved.State)
	}
	expectedGrace := now.Add(graceDuration)
	if !saved.GraceExpiry.Equal(expectedGrace) {
		t.Errorf("expected GraceExpiry %v, got %v", expectedGrace, saved.GraceExpiry)
	}
}

// 3. Graceful shutdown error triggers DestroyDomain fallback.
func TestLeaseManager_ShutdownFallbackToDestroy(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{
		shutdownErr: errors.New("ACPI shutdown refused by guest agent"),
	}
	notifier := &mockAlertNotifier{}

	now := time.Now().UTC()
	lease := Lease{
		RequestID:   "req-fail-sd",
		MachineUUID: "vm-uuid-fail-sd",
		ProjectID:   "proj-1",
		Expiry:      now.Add(-1 * time.Hour),
		State:       LeaseStateActive,
	}
	_ = store.SaveLease(lease)

	mgr := NewLeaseManager(store, ctrl, notifier, nil)
	events := mgr.Sweep(context.Background(), now)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if len(ctrl.shutdowns) != 1 {
		t.Errorf("expected 1 shutdown call, got %d", len(ctrl.shutdowns))
	}
	if len(ctrl.destroys) != 1 || ctrl.destroys[0] != "vm-uuid-fail-sd" {
		t.Errorf("expected fallback DestroyDomain on 'vm-uuid-fail-sd', got %v", ctrl.destroys)
	}

	saved, _ := store.GetLease("vm-uuid-fail-sd")
	if saved.State != LeaseStateGracePeriod {
		t.Errorf("expected grace_period state, got %q", saved.State)
	}
}

// 4. Graceful shutdown timeout triggers DestroyDomain fallback.
func TestLeaseManager_ShutdownTimeoutFallback(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{
		shutdownDelay: 100 * time.Millisecond,
	}
	notifier := &mockAlertNotifier{}

	now := time.Now().UTC()
	lease := Lease{
		RequestID:   "req-to-sd",
		MachineUUID: "vm-uuid-to-sd",
		ProjectID:   "proj-1",
		Expiry:      now.Add(-1 * time.Hour),
		State:       LeaseStateActive,
	}
	_ = store.SaveLease(lease)

	mgr := NewLeaseManager(store, ctrl, notifier, nil,
		WithShutdownTimeout(20*time.Millisecond),
	)

	mgr.Sweep(context.Background(), now)

	if len(ctrl.destroys) != 1 || ctrl.destroys[0] != "vm-uuid-to-sd" {
		t.Errorf("expected fallback DestroyDomain on timeout, got %v", ctrl.destroys)
	}
}

// 5. Grace period expiry calls DeleteDomain, purges machine, marks lease terminated and request expired.
func TestLeaseManager_GracePeriodExpiryPurge(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	// Create request
	req := Request{
		ID:          "req-purge-1",
		BlueprintID: "bp-test",
		ProjectID:   "proj-1",
		Requester:   "alice",
		VMName:      "vm-purge-target",
		MachineUUID: "vm-uuid-purge-1",
		Status:      StatusActive,
		CreatedAt:   now.Add(-10 * 24 * time.Hour),
	}
	savedReq, err := store.CreateRequest(req)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	// Create lease in grace_period where GraceExpiry is in the past.
	lease := Lease{
		RequestID:       savedReq.ID,
		MachineUUID:     "vm-uuid-purge-1",
		ProjectID:       "proj-1",
		Expiry:          now.Add(-2 * 24 * time.Hour),
		GraceExpiry:     now.Add(-1 * time.Hour),
		State:           LeaseStateGracePeriod,
		ReminderSent:    true,
		LastStateChange: now.Add(-24 * time.Hour),
	}
	if err := store.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	mgr := NewLeaseManager(store, ctrl, notifier, nil)

	events := mgr.Sweep(context.Background(), now)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "purge" {
		t.Errorf("expected event type 'purge', got %q", events[0].Type)
	}

	// Verify DeleteDomain was called.
	if len(ctrl.deletes) != 1 || ctrl.deletes[0] != "vm-uuid-purge-1" {
		t.Errorf("expected DeleteDomain on 'vm-uuid-purge-1', got %v", ctrl.deletes)
	}

	// Verify lease is terminated.
	savedLease, ok := store.GetLease("vm-uuid-purge-1")
	if !ok {
		t.Fatal("lease not found in store")
	}
	if savedLease.State != LeaseStateTerminated {
		t.Errorf("expected lease state 'terminated', got %q", savedLease.State)
	}

	// Verify request is expired.
	updatedReq, ok := store.GetRequest(savedReq.ID)
	if !ok {
		t.Fatal("request not found in store")
	}
	if updatedReq.Status != StatusExpired {
		t.Errorf("expected request status 'expired', got %q", updatedReq.Status)
	}

	// Verify notification.
	dom, title, _ := notifier.last()
	if dom != "vm-uuid-purge-1" {
		t.Errorf("expected domain %q, got %q", "vm-uuid-purge-1", dom)
	}
	if title != "Machine Lease Terminated - Deleted" {
		t.Errorf("expected title 'Machine Lease Terminated - Deleted', got %q", title)
	}
}

// 6. ExtendLease extends expiry, clears grace period back to active, and resets reminder flag.
func TestLeaseManager_ExtendLease_ClearsGracePeriodAndResetsReminder(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Now().UTC()

	req := Request{
		ID:          "req-ext-1",
		BlueprintID: "bp-unlimited",
		ProjectID:   "proj-finance",
		Requester:   "alice",
		VMName:      "vm-ext-target",
		MachineUUID: "vm-uuid-ext-1",
		Status:      StatusActive,
		LeaseDays:   7,
		CreatedAt:   now.Add(-7 * 24 * time.Hour),
	}
	savedReq, err := store.CreateRequest(req)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	// Lease currently in grace period
	lease := Lease{
		RequestID:       savedReq.ID,
		MachineUUID:     "vm-uuid-ext-1",
		ProjectID:       "proj-finance",
		Expiry:          now.Add(-2 * time.Hour),
		GraceExpiry:     now.Add(22 * time.Hour),
		State:           LeaseStateGracePeriod,
		ReminderSent:    true,
		LastStateChange: now.Add(-2 * time.Hour),
	}
	if err := store.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	mgr := NewLeaseManager(store, ctrl, notifier, nil)

	// Caller is the requester "alice"
	extended, err := mgr.ExtendLease(savedReq.ID, "alice", auth.Viewer, nil, 7)
	if err != nil {
		t.Fatalf("ExtendLease failed: %v", err)
	}

	if extended.State != LeaseStateActive {
		t.Errorf("expected State %q, got %q", LeaseStateActive, extended.State)
	}
	if extended.ReminderSent {
		t.Error("expected ReminderSent = false")
	}
	if !extended.GraceExpiry.IsZero() {
		t.Errorf("expected zero GraceExpiry, got %v", extended.GraceExpiry)
	}
	expectedMinExpiry := now.Add(6 * 24 * time.Hour)
	if extended.Expiry.Before(expectedMinExpiry) {
		t.Errorf("expected new expiry > %v, got %v", expectedMinExpiry, extended.Expiry)
	}

	// Verify store state
	savedInStore, _ := store.GetLease("vm-uuid-ext-1")
	if savedInStore.State != LeaseStateActive || savedInStore.ReminderSent {
		t.Errorf("store lease was not properly updated: %+v", savedInStore)
	}
}

// 7. ExtendLease enforces MaxLeaseDays upper bound and caller permissions.
func TestLeaseManager_ExtendLease_MaxLeaseDaysAndPermissions(t *testing.T) {
	store := setupTestLeaseStore(t)
	projStore := setupTestProjectStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Now().UTC()

	// Create blueprint with MaxLeaseDays = 14
	bp := Blueprint{
		ID:           "bp-capped",
		Name:         "Capped Blueprint",
		SourceType:   SourceTypeImage,
		SourceRef:    "/img/base.qcow2",
		MaxLeaseDays: 14,
		Presets: []SizePreset{
			{ID: "small", Name: "Small", VCPUs: 1, MemoryMiB: 1024, DiskGiB: 10},
		},
	}
	if _, err := store.CreateBlueprint(bp); err != nil {
		t.Fatalf("CreateBlueprint: %v", err)
	}

	// Request created with 7 days initial lease
	req := Request{
		ID:          "req-capped-1",
		BlueprintID: "bp-capped",
		ProjectID:   "proj-finance",
		Requester:   "alice",
		VMName:      "vm-capped",
		MachineUUID: "vm-uuid-capped",
		Status:      StatusActive,
		LeaseDays:   7,
		CreatedAt:   now,
	}
	savedReq, err := store.CreateRequest(req)
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	lease := Lease{
		RequestID:   savedReq.ID,
		MachineUUID: "vm-uuid-capped",
		ProjectID:   "proj-finance",
		Expiry:      now.Add(7 * 24 * time.Hour),
		State:       LeaseStateActive,
	}
	if err := store.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	mgr := NewLeaseManager(store, ctrl, notifier, nil,
		WithProjects(projStore),
	)

	// Permission checks:
	// 1. Random unauthorized user "stranger" should fail.
	_, err = mgr.ExtendLease(savedReq.ID, "stranger", auth.Viewer, nil, 3)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for stranger, got %v", err)
	}

	// 2. Project viewer "dave" (not requester) should fail.
	_, err = mgr.ExtendLease(savedReq.ID, "dave", auth.Viewer, nil, 3)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for project viewer dave, got %v", err)
	}

	// 3. Project operator "bob" (not requester, not admin) should fail.
	_, err = mgr.ExtendLease(savedReq.ID, "bob", auth.Viewer, nil, 3)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized for non-requester operator bob, got %v", err)
	}

	// 4. Project admin "carol" should be allowed.
	extended, err := mgr.ExtendLease(savedReq.ID, "carol", auth.Viewer, nil, 3)
	if err != nil {
		t.Fatalf("expected project admin carol to be allowed, got error: %v", err)
	}
	if extended.State != LeaseStateActive {
		t.Errorf("expected active state, got %q", extended.State)
	}

	// 4. Global admin should be allowed.
	_, err = mgr.ExtendLease(savedReq.ID, "superadmin", auth.Admin, nil, 2)
	if err != nil {
		t.Fatalf("expected global admin to be allowed, got error: %v", err)
	}

	// 5. Requester "alice" should be allowed.
	// Current total lease days is 7 + 3 + 2 = 12.
	// Extending by 5 days would make total 17 days > 14 (MaxLeaseDays) -> should fail with ErrInvalid.
	_, err = mgr.ExtendLease(savedReq.ID, "alice", auth.Viewer, nil, 5)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid when exceeding MaxLeaseDays, got %v", err)
	}

	// 6. Extending by 2 days makes total 14 days == 14 -> should succeed!
	_, err = mgr.ExtendLease(savedReq.ID, "alice", auth.Viewer, nil, 2)
	if err != nil {
		t.Fatalf("expected extension up to max 14 days to succeed, got %v", err)
	}
}

// 8. ExtendLease validates positive days and non-existent IDs.
func TestLeaseManager_ExtendLease_Validations(t *testing.T) {
	store := setupTestLeaseStore(t)
	mgr := NewLeaseManager(store, nil, nil, nil)

	// Missing request
	_, err := mgr.ExtendLease("missing-req", "alice", auth.Admin, nil, 5)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing request, got %v", err)
	}

	// Negative or zero additional days
	req, err := store.CreateRequest(Request{
		ID:          "req-valid-1",
		BlueprintID: "bp-valid",
		ProjectID:   "proj-1",
		Requester:   "alice",
		VMName:      "vm-valid",
		MachineUUID: "vm-uuid-valid",
		Status:      StatusActive,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	_ = store.SaveLease(Lease{
		RequestID:   req.ID,
		MachineUUID: "vm-uuid-valid",
		ProjectID:   "proj-1",
		State:       LeaseStateActive,
		Expiry:      time.Now().Add(24 * time.Hour),
	})

	_, err = mgr.ExtendLease(req.ID, "alice", auth.Admin, nil, 0)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for 0 additional days, got %v", err)
	}

	_, err = mgr.ExtendLease(req.ID, "alice", auth.Admin, nil, -3)
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for negative additional days, got %v", err)
	}
}

//  9. DeleteDomain failure during purge leaves lease in grace_period for retry,
//     does not mark the request expired, and sends no "deleted" notification.
func TestLeaseManager_PurgeDeleteDomainErrorRetries(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{
		deleteErr: errors.New("storage backend unavailable"),
	}
	notifier := &mockAlertNotifier{}

	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	savedReq, err := store.CreateRequest(Request{
		ID:          "req-purge-fail-1",
		BlueprintID: "bp-test",
		ProjectID:   "proj-1",
		Requester:   "alice",
		VMName:      "vm-purge-fail",
		MachineUUID: "vm-uuid-purge-fail",
		Status:      StatusActive,
		CreatedAt:   now.Add(-10 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	lease := Lease{
		RequestID:       savedReq.ID,
		MachineUUID:     "vm-uuid-purge-fail",
		ProjectID:       "proj-1",
		Expiry:          now.Add(-2 * 24 * time.Hour),
		GraceExpiry:     now.Add(-1 * time.Hour),
		State:           LeaseStateGracePeriod,
		ReminderSent:    true,
		LastStateChange: now.Add(-24 * time.Hour),
	}
	if err := store.SaveLease(lease); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	mgr := NewLeaseManager(store, ctrl, notifier, nil)

	// First sweep: DeleteDomain fails.
	events := mgr.Sweep(context.Background(), now)
	if len(events) != 0 {
		t.Fatalf("expected 0 events when DeleteDomain fails, got %d", len(events))
	}
	if len(ctrl.deletes) != 1 || ctrl.deletes[0] != "vm-uuid-purge-fail" {
		t.Errorf("expected DeleteDomain on 'vm-uuid-purge-fail', got %v", ctrl.deletes)
	}

	// Lease remains in grace_period so deletion is retried on the next sweep.
	savedLease, ok := store.GetLease("vm-uuid-purge-fail")
	if !ok {
		t.Fatal("lease not found in store")
	}
	if savedLease.State != LeaseStateGracePeriod {
		t.Errorf("expected lease state %q after failed delete, got %q", LeaseStateGracePeriod, savedLease.State)
	}

	// Request is not marked expired until deletion succeeds.
	updatedReq, ok := store.GetRequest(savedReq.ID)
	if !ok {
		t.Fatal("request not found in store")
	}
	if updatedReq.Status != StatusActive {
		t.Errorf("expected request status %q after failed delete, got %q", StatusActive, updatedReq.Status)
	}

	// No "permanently deleted" notification goes out on failure.
	if notifier.count() != 0 {
		t.Errorf("expected 0 notifications after failed delete, got %d", notifier.count())
	}

	// Retry sweep after storage failure clears: purge succeeds.
	ctrl.mu.Lock()
	ctrl.deleteErr = nil
	ctrl.mu.Unlock()

	retryNow := now.Add(5 * time.Minute)
	retryEvents := mgr.Sweep(context.Background(), retryNow)
	if len(retryEvents) != 1 {
		t.Fatalf("expected 1 purge event on retry sweep, got %d", len(retryEvents))
	}
	if retryEvents[0].Type != "purge" {
		t.Errorf("expected event type 'purge', got %q", retryEvents[0].Type)
	}

	finalLease, ok := store.GetLease("vm-uuid-purge-fail")
	if !ok {
		t.Fatal("lease not found in store after retry")
	}
	if finalLease.State != LeaseStateTerminated {
		t.Errorf("expected lease state %q after successful delete, got %q", LeaseStateTerminated, finalLease.State)
	}
	finalReq, ok := store.GetRequest(savedReq.ID)
	if !ok {
		t.Fatal("request not found in store after retry")
	}
	if finalReq.Status != StatusExpired {
		t.Errorf("expected request status %q after successful delete, got %q", StatusExpired, finalReq.Status)
	}
	if notifier.count() != 1 {
		t.Errorf("expected 1 notification after successful delete, got %d", notifier.count())
	}
}

// 10. Terminated leases cannot be extended (machine has already been purged).
func TestLeaseManager_ExtendLease_TerminatedRejected(t *testing.T) {
	store := setupTestLeaseStore(t)
	mgr := NewLeaseManager(store, &mockDomainController{}, &mockAlertNotifier{}, nil)

	now := time.Now().UTC()
	savedReq, err := store.CreateRequest(Request{
		ID:          "req-term-ext-1",
		BlueprintID: "bp-test",
		ProjectID:   "proj-1",
		Requester:   "alice",
		VMName:      "vm-term-ext",
		MachineUUID: "vm-uuid-term-ext",
		Status:      StatusActive,
		CreatedAt:   now.Add(-10 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if err := store.SaveLease(Lease{
		RequestID:   savedReq.ID,
		MachineUUID: "vm-uuid-term-ext",
		ProjectID:   "proj-1",
		Expiry:      now.Add(-1 * time.Hour),
		State:       LeaseStateTerminated,
	}); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	_, err = mgr.ExtendLease(savedReq.ID, "alice", auth.Admin, nil, 5)
	if err == nil {
		t.Fatal("expected error when extending a terminated lease, got nil")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid when extending terminated lease, got %v", err)
	}

	// Lease must remain terminated and unchanged.
	saved, ok := store.GetLease("vm-uuid-term-ext")
	if !ok {
		t.Fatal("lease not found in store")
	}
	if saved.State != LeaseStateTerminated {
		t.Errorf("expected lease to remain %q, got %q", LeaseStateTerminated, saved.State)
	}
}

// 11. Sweep with an already-cancelled context performs no actions.
func TestLeaseManager_SweepContextCancelled(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Now().UTC()
	savedReq, err := store.CreateRequest(Request{
		ID:          "req-ctx-cancel",
		BlueprintID: "bp-test",
		ProjectID:   "proj-1",
		Requester:   "alice",
		VMName:      "vm-ctx-cancel",
		MachineUUID: "vm-uuid-ctx-cancel",
		Status:      StatusActive,
		CreatedAt:   now.Add(-10 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if err := store.SaveLease(Lease{
		RequestID:   savedReq.ID,
		MachineUUID: "vm-uuid-ctx-cancel",
		ProjectID:   "proj-1",
		Expiry:      now.Add(-1 * time.Hour),
		State:       LeaseStateActive,
	}); err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	mgr := NewLeaseManager(store, ctrl, notifier, nil,
		WithShutdownTimeout(10*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events := mgr.Sweep(ctx, now)
	if len(events) != 0 {
		t.Errorf("expected 0 events with cancelled context, got %d", len(events))
	}
	if len(ctrl.shutdowns) != 0 {
		t.Errorf("expected no ShutdownDomain calls with cancelled context, got %v", ctrl.shutdowns)
	}
	if len(ctrl.destroys) != 0 {
		t.Errorf("expected no DestroyDomain calls with cancelled context, got %v", ctrl.destroys)
	}

	// Lease remains untouched.
	saved, ok := store.GetLease("vm-uuid-ctx-cancel")
	if !ok {
		t.Fatal("lease not found in store")
	}
	if saved.State != LeaseStateActive {
		t.Errorf("expected lease state %q unchanged, got %q", LeaseStateActive, saved.State)
	}
	if !saved.GraceExpiry.IsZero() {
		t.Errorf("expected zero GraceExpiry unchanged, got %v", saved.GraceExpiry)
	}
}

// 12. Run ticker executes sweep and terminates on ctx.Done.
func TestLeaseManager_Run(t *testing.T) {
	store := setupTestLeaseStore(t)
	ctrl := &mockDomainController{}
	notifier := &mockAlertNotifier{}

	now := time.Now().UTC()
	_ = store.SaveLease(Lease{
		RequestID:   "req-ticker",
		MachineUUID: "vm-ticker",
		ProjectID:   "proj-1",
		Expiry:      now.Add(-1 * time.Hour),
		State:       LeaseStateActive,
	})

	mgr := NewLeaseManager(store, ctrl, notifier, nil,
		WithInterval(10*time.Millisecond),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Succeeded and exited cleanly
	case <-time.After(1 * time.Second):
		t.Fatal("mgr.Run did not terminate on context timeout")
	}

	if len(ctrl.shutdowns) == 0 {
		t.Error("expected at least one sweep to have executed during Run")
	}
}
