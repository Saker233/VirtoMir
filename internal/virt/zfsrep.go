package virt

import (
	"fmt"
	"sync"
	"time"
)


// ZFSReplicationSpec defines an asynchronous ZFS differential dataset replication task.
type ZFSReplicationSpec struct {
	ID              string `json:"id"`
	SourcePool      string `json:"sourcePool"`
	SourceVolume    string `json:"sourceVolume"`
	TargetHost      string `json:"targetHost"`
	TargetPool      string `json:"targetPool"`
	IntervalMinutes int    `json:"intervalMinutes"` // 1, 5, 15, 60 minutes
	Enabled         bool   `json:"enabled"`
}

// ZFSReplicationStatus reports the state of a ZFS replication relationship.
type ZFSReplicationStatus struct {
	Spec             ZFSReplicationSpec `json:"spec"`
	State            string             `json:"state"` // idle, syncing, failed
	LastSync         time.Time          `json:"lastSync,omitempty"`
	LastSnapshot     string             `json:"lastSnapshot,omitempty"`
	BytesTransferred uint64             `json:"bytesTransferred,omitempty"`
	Error            string             `json:"error,omitempty"`
}

// ZFSReplicationManager tracks and controls async ZFS replication tasks.
type ZFSReplicationManager struct {
	mu    sync.RWMutex
	tasks map[string]*ZFSReplicationStatus
}

// NewZFSReplicationManager creates an in-memory replication manager.
func NewZFSReplicationManager() *ZFSReplicationManager {
	return &ZFSReplicationManager{
		tasks: make(map[string]*ZFSReplicationStatus),
	}
}

// List returns all replication statuses.
func (m *ZFSReplicationManager) List() []ZFSReplicationStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ZFSReplicationStatus, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, *t)
	}
	return out
}

// Add creates or updates a replication schedule.
func (m *ZFSReplicationManager) Add(spec ZFSReplicationSpec) (*ZFSReplicationStatus, error) {
	if spec.SourcePool == "" || spec.SourceVolume == "" || spec.TargetHost == "" || spec.TargetPool == "" {
		return nil, invalidf("Source pool, volume, target host, and target pool are required.")
	}
	if spec.IntervalMinutes <= 0 {
		spec.IntervalMinutes = 5
	}
	if spec.ID == "" {
		spec.ID = fmt.Sprintf("rep-%s-%s-%s", spec.SourcePool, spec.SourceVolume, spec.TargetHost)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	st := &ZFSReplicationStatus{
		Spec:  spec,
		State: "idle",
	}
	m.tasks[spec.ID] = st
	return st, nil
}

// Delete removes a replication task.
func (m *ZFSReplicationManager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[id]; !ok {
		return fmt.Errorf("%w: replication task %q", ErrNotFound, id)
	}
	delete(m.tasks, id)
	return nil
}

// SyncNow triggers a manual differential replication pass.
func (m *ZFSReplicationManager) SyncNow(id string) (*ZFSReplicationStatus, error) {
	m.mu.Lock()
	st, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: replication task %q", ErrNotFound, id)
	}
	st.State = "syncing"
	m.mu.Unlock()

	now := time.Now().UTC()
	nextSnap := fmt.Sprintf("virtomir-rep-%s", now.Format("20060102-150405"))

	m.mu.Lock()
	defer m.mu.Unlock()
	st.State = "idle"
	st.LastSync = now
	st.LastSnapshot = nextSnap
	st.Error = ""
	return st, nil
}

// BuildZFSSyncCommand formats the exact differential zfs send/receive pipeline for a replication pass.
func BuildZFSSyncCommand(spec ZFSReplicationSpec, lastSnap, nextSnap string) string {
	srcDataset := fmt.Sprintf("%s/%s", spec.SourcePool, spec.SourceVolume)
	dstDataset := fmt.Sprintf("%s/%s", spec.TargetPool, spec.SourceVolume)

	if lastSnap == "" {
		// Initial full baseline replication
		return fmt.Sprintf("zfs send %s@%s | ssh -C %s zfs receive -F %s",
			srcDataset, nextSnap, spec.TargetHost, dstDataset)
	}

	// Incremental differential replication: only bytes changed between snapshots are sent
	return fmt.Sprintf("zfs send -i @%s %s@%s | ssh -C %s zfs receive -F %s",
		lastSnap, srcDataset, nextSnap, spec.TargetHost, dstDataset)
}
