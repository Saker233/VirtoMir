package virt

import (
	"strings"
	"testing"
)

func TestBuildZFSSyncCommand(t *testing.T) {
	spec := ZFSReplicationSpec{
		SourcePool:   "tank",
		SourceVolume: "vm-101-disk-0",
		TargetHost:   "192.168.77.20",
		TargetPool:   "rpool",
	}

	// Baseline sync
	cmdBase := BuildZFSSyncCommand(spec, "", "snap1")
	if !strings.Contains(cmdBase, "zfs send tank/vm-101-disk-0@snap1") || !strings.Contains(cmdBase, "ssh -C 192.168.77.20 zfs receive -F rpool/vm-101-disk-0") {
		t.Errorf("unexpected baseline command: %q", cmdBase)
	}

	// Incremental sync
	cmdInc := BuildZFSSyncCommand(spec, "snap1", "snap2")
	if !strings.Contains(cmdInc, "zfs send -i @snap1 tank/vm-101-disk-0@snap2") {
		t.Errorf("unexpected incremental command: %q", cmdInc)
	}
}

func TestZFSReplicationManager(t *testing.T) {
	m := NewZFSReplicationManager()

	spec := ZFSReplicationSpec{
		SourcePool:      "tank",
		SourceVolume:    "disk-a",
		TargetHost:      "node-2",
		TargetPool:      "backup-pool",
		IntervalMinutes: 5,
		Enabled:         true,
	}

	st, err := m.Add(spec)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if st.Spec.ID == "" {
		t.Errorf("expected generated ID")
	}

	list := m.List()
	if len(list) != 1 || list[0].Spec.SourcePool != "tank" {
		t.Errorf("unexpected list: %+v", list)
	}

	synced, err := m.SyncNow(st.Spec.ID)
	if err != nil {
		t.Fatalf("SyncNow failed: %v", err)
	}
	if synced.LastSnapshot == "" || synced.LastSync.IsZero() {
		t.Errorf("expected LastSnapshot and LastSync populated: %+v", synced)
	}

	if err := m.Delete(st.Spec.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if len(m.List()) != 0 {
		t.Errorf("expected empty list after deletion")
	}
}
