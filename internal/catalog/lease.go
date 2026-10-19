package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
)

// AlertNotifier notifies users and administrators of lease lifecycle events.
type AlertNotifier interface {
	Notice(domain, title, message string)
}

// DomainController controls machine lifecycle operations for lease enforcement.
type DomainController interface {
	ShutdownDomain(uuid string) error
	DestroyDomain(uuid string) error
	DeleteDomain(uuid string) error
}

// ProjectStore provides project membership and role resolution.
type ProjectStore interface {
	RoleFor(projectID, user string, groups []string) (string, bool)
}

// LeaseEvent records an action performed during a lease lifecycle sweep.
type LeaseEvent struct {
	Type        string    `json:"type"` // "reminder", "shutdown", "purge"
	MachineUUID string    `json:"machineUuid"`
	RequestID   string    `json:"requestId"`
	ProjectID   string    `json:"projectId"`
	Message     string    `json:"message"`
	Time        time.Time `json:"time"`
}

// LeaseOption configures a LeaseManager instance.
type LeaseOption func(*LeaseManager)

// WithReminderBefore sets the duration before lease expiry when a warning reminder is sent.
func WithReminderBefore(d time.Duration) LeaseOption {
	return func(m *LeaseManager) {
		m.reminderBefore = d
	}
}

// WithGracePeriod sets the grace period duration after lease expiry before a machine is purged.
func WithGracePeriod(d time.Duration) LeaseOption {
	return func(m *LeaseManager) {
		m.gracePeriod = d
	}
}

// WithInterval sets the periodic sweep check interval.
func WithInterval(d time.Duration) LeaseOption {
	return func(m *LeaseManager) {
		m.interval = d
	}
}

// WithShutdownTimeout sets the timeout for graceful ACPI shutdown before falling back to destroy.
func WithShutdownTimeout(d time.Duration) LeaseOption {
	return func(m *LeaseManager) {
		m.shutdownTimeout = d
	}
}

// WithProjects sets the project store for project-level permission checks.
func WithProjects(ps ProjectStore) LeaseOption {
	return func(m *LeaseManager) {
		m.projects = ps
	}
}

// WithProjectStore is an alias for WithProjects.
func WithProjectStore(ps ProjectStore) LeaseOption {
	return WithProjects(ps)
}

// LeaseManager monitors and enforces machine lease lifecycles, warning reminders,
// automated guest shutdowns, and grace-period machine purging.
type LeaseManager struct {
	sweepMu         sync.Mutex
	mu              sync.Mutex
	store           *Store
	domainCtrl      DomainController
	notifier        AlertNotifier
	projects        ProjectStore
	log             *slog.Logger
	reminderBefore  time.Duration
	gracePeriod     time.Duration
	interval        time.Duration
	shutdownTimeout time.Duration
}

// NewLeaseManager initializes a new LeaseManager with defaults (24h reminder, 24h grace period, 5m interval).
func NewLeaseManager(store *Store, ctrl DomainController, notifier AlertNotifier, log *slog.Logger, opts ...LeaseOption) *LeaseManager {
	if log == nil {
		log = slog.Default()
	}
	m := &LeaseManager{
		store:           store,
		domainCtrl:      ctrl,
		notifier:        notifier,
		log:             log,
		reminderBefore:  24 * time.Hour,
		gracePeriod:     24 * time.Hour,
		interval:        5 * time.Minute,
		shutdownTimeout: 5 * time.Second,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Sweep checks active and grace-period leases, sending warning reminders, shutting down
// expired machines into grace period, and permanently purging machines after grace expiry.
func (m *LeaseManager) Sweep(ctx context.Context, now time.Time) []LeaseEvent {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()

	var events []LeaseEvent
	if m.store == nil {
		return events
	}
	if err := ctx.Err(); err != nil {
		return events
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	leases := m.store.ListActiveLeases()
	for _, lease := range leases {
		if err := ctx.Err(); err != nil {
			return events
		}

		// Re-fetch lease to operate on the latest state.
		cur, ok := m.store.GetLease(lease.MachineUUID)
		if !ok {
			continue
		}

		// Phase 3: Grace Period Expiration & Machine Purge
		if cur.State == LeaseStateGracePeriod && now.After(cur.GraceExpiry) {
			if m.domainCtrl != nil {
				if err := m.domainCtrl.DeleteDomain(cur.MachineUUID); err != nil {
					if m.log != nil {
						m.log.Warn("lease purge: delete domain failed, will retry on next sweep", "uuid", cur.MachineUUID, "err", err)
					}
					// Do not transition to terminated or send "deleted" notification if deletion failed.
					// Leave lease in grace_period to retry on subsequent sweeps.
					continue
				}
			}
			cur.State = LeaseStateTerminated
			cur.LastStateChange = now
			if err := m.store.SaveLease(cur); err != nil && m.log != nil {
				m.log.Warn("lease purge: save lease failed", "uuid", cur.MachineUUID, "err", err)
			}
			if req, ok := m.store.GetRequest(cur.RequestID); ok {
				req.Status = StatusExpired
				if err := m.store.UpdateRequest(req); err != nil && m.log != nil {
					m.log.Warn("lease purge: update request status failed", "id", req.ID, "err", err)
				}
			}
			if m.notifier != nil {
				m.notifier.Notice(cur.MachineUUID, "Machine Lease Terminated - Deleted",
					fmt.Sprintf("Machine %s in project %s grace period has ended. The machine has been permanently deleted.", cur.MachineUUID, cur.ProjectID))
			}
			events = append(events, LeaseEvent{
				Type:        "purge",
				MachineUUID: cur.MachineUUID,
				RequestID:   cur.RequestID,
				ProjectID:   cur.ProjectID,
				Message:     fmt.Sprintf("Machine %s grace period expired; domain deleted and lease terminated", cur.MachineUUID),
				Time:        now,
			})
			continue
		}

		// Phase 2: Expiry & Automatic Shutdown (Grace Period entry)
		if (cur.State == LeaseStateActive || cur.State == LeaseStateReminderSent) && now.After(cur.Expiry) {
			if m.domainCtrl != nil {
				m.shutdownDomain(ctx, cur.MachineUUID)
			}
			if err := ctx.Err(); err != nil {
				return events
			}

			// Re-fetch lease to make sure it was not extended while shutdown was executing.
			latest, ok := m.store.GetLease(cur.MachineUUID)
			if !ok || latest.State == LeaseStateTerminated || (latest.State == LeaseStateActive && !now.After(latest.Expiry)) {
				continue
			}

			latest.State = LeaseStateGracePeriod
			latest.GraceExpiry = now.Add(m.gracePeriod)
			latest.LastStateChange = now
			if err := m.store.SaveLease(latest); err != nil && m.log != nil {
				m.log.Warn("lease shutdown: save lease failed", "uuid", latest.MachineUUID, "err", err)
			}
			if m.notifier != nil {
				m.notifier.Notice(latest.MachineUUID, "Machine Lease Expired - Powered Off",
					fmt.Sprintf("Machine %s in project %s lease expired at %s and was powered off. You have until %s (%s grace period) to extend the lease before permanent deletion.",
						latest.MachineUUID, latest.ProjectID, latest.Expiry.UTC().Format(time.RFC3339), latest.GraceExpiry.UTC().Format(time.RFC3339), m.gracePeriod))
			}
			events = append(events, LeaseEvent{
				Type:        "shutdown",
				MachineUUID: latest.MachineUUID,
				RequestID:   latest.RequestID,
				ProjectID:   latest.ProjectID,
				Message:     fmt.Sprintf("Machine %s lease expired, powered off, grace period ends %s", latest.MachineUUID, latest.GraceExpiry.UTC().Format(time.RFC3339)),
				Time:        now,
			})
			continue
		}

		// Phase 1: Expiry Warning Reminder
		if cur.State == LeaseStateActive && !cur.ReminderSent {
			if now.Add(m.reminderBefore).After(cur.Expiry) {
				if m.notifier != nil {
					m.notifier.Notice(cur.MachineUUID, "Machine Lease Expiring Soon",
						fmt.Sprintf("Machine %s in project %s will expire at %s. Extend the lease in the self-service catalog to prevent shutdown.",
							cur.MachineUUID, cur.ProjectID, cur.Expiry.UTC().Format(time.RFC3339)))
				}
				cur.ReminderSent = true
				cur.State = LeaseStateReminderSent
				cur.LastStateChange = now
				if err := m.store.SaveLease(cur); err != nil && m.log != nil {
					m.log.Warn("lease reminder: save lease failed", "uuid", cur.MachineUUID, "err", err)
				}
				events = append(events, LeaseEvent{
					Type:        "reminder",
					MachineUUID: cur.MachineUUID,
					RequestID:   cur.RequestID,
					ProjectID:   cur.ProjectID,
					Message:     fmt.Sprintf("Machine %s lease reminder sent", cur.MachineUUID),
					Time:        now,
				})
			}
		}
	}

	return events
}

func (m *LeaseManager) shutdownDomain(ctx context.Context, uuid string) {
	if m.domainCtrl == nil {
		return
	}
	done := make(chan error, 1)
	go func() {
		done <- m.domainCtrl.ShutdownDomain(uuid)
	}()

	timeout := m.shutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	select {
	case err := <-done:
		if err != nil {
			if m.log != nil {
				m.log.Warn("graceful shutdown failed, forcing destroy", "machine", uuid, "err", err)
			}
			if destroyErr := m.domainCtrl.DestroyDomain(uuid); destroyErr != nil && m.log != nil {
				m.log.Warn("destroy domain failed", "machine", uuid, "err", destroyErr)
			}
		}
	case <-time.After(timeout):
		if m.log != nil {
			m.log.Warn("graceful shutdown timed out, forcing destroy", "machine", uuid)
		}
		if destroyErr := m.domainCtrl.DestroyDomain(uuid); destroyErr != nil && m.log != nil {
			m.log.Warn("destroy domain failed", "machine", uuid, "err", destroyErr)
		}
	case <-ctx.Done():
		if m.log != nil {
			m.log.Debug("shutdown aborted due to context cancellation", "machine", uuid, "err", ctx.Err())
		}
		return
	}
}

// ExtendLease extends a machine's lease duration if authorized and within blueprint max lease limits.
func (m *LeaseManager) ExtendLease(requestID, user string, role auth.Role, groups []string, additionalDays int) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.store == nil {
		return Lease{}, invalidf("catalog store not configured")
	}

	req, ok := m.store.GetRequest(requestID)
	if !ok {
		return Lease{}, notFoundf("request %q not found", requestID)
	}

	lease, ok := m.store.GetLeaseByRequest(requestID)
	if !ok && req.MachineUUID != "" {
		lease, ok = m.store.GetLease(req.MachineUUID)
	}
	if !ok {
		return Lease{}, notFoundf("lease for request %q not found", requestID)
	}

	// Terminated leases cannot be extended (machine has already been purged).
	if lease.State == LeaseStateTerminated {
		return Lease{}, invalidf("cannot extend terminated lease for machine %q", lease.MachineUUID)
	}

	// Caller must be request.Requester, project admin in request.ProjectID, or global admin.
	isGlobalAdmin := role >= auth.Admin
	isRequester := user != "" && user == req.Requester
	isProjectAdmin := false
	if m.projects != nil {
		pRole, isMember := m.projects.RoleFor(req.ProjectID, user, groups)
		if isMember && pRole == "admin" {
			isProjectAdmin = true
		}
	}
	if !isGlobalAdmin && !isRequester && !isProjectAdmin {
		return Lease{}, fmt.Errorf("%w: caller %q is not authorized to extend lease for request %q (must be requester, project admin, or global admin)", ErrUnauthorized, user, requestID)
	}

	if additionalDays <= 0 {
		return Lease{}, invalidf("additional days must be positive (got %d)", additionalDays)
	}

	now := time.Now().UTC()
	var newExpiry time.Time
	if lease.State == LeaseStateGracePeriod || now.After(lease.Expiry) {
		newExpiry = now.Add(time.Duration(additionalDays) * 24 * time.Hour)
	} else {
		newExpiry = lease.Expiry.Add(time.Duration(additionalDays) * 24 * time.Hour)
	}

	// Blueprint MaxLeaseDays: total days from CreatedAt cannot exceed MaxLeaseDays.
	if bp, ok := m.store.GetBlueprint(req.BlueprintID); ok && bp.MaxLeaseDays > 0 {
		createdAt := req.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		maxExpiry := createdAt.Add(time.Duration(bp.MaxLeaseDays) * 24 * time.Hour)
		if newExpiry.After(maxExpiry.Add(time.Second)) || (req.LeaseDays > 0 && req.LeaseDays+additionalDays > bp.MaxLeaseDays) {
			return Lease{}, invalidf("total lease duration exceeds blueprint maximum of %d days", bp.MaxLeaseDays)
		}
	}

	lease.State = LeaseStateActive
	lease.ReminderSent = false
	lease.Expiry = newExpiry
	lease.GraceExpiry = time.Time{}
	lease.LastStateChange = now

	if req.Status == StatusExpired || req.Status == StatusFailed {
		req.Status = StatusActive
	}
	req.LeaseDays += additionalDays

	if err := m.store.SaveLease(lease); err != nil {
		return Lease{}, err
	}
	if err := m.store.UpdateRequest(req); err != nil {
		return Lease{}, err
	}

	return lease, nil
}

// Run starts the periodic background sweep for lease lifecycle management until ctx is cancelled.
func (m *LeaseManager) Run(ctx context.Context) {
	interval := m.interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.Sweep(ctx, now)
		}
	}
}
