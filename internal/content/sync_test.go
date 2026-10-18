package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/jobs"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// TestSubscriptionMatcher tests subscription matching:
// - matches mode "all"
// - matches specific host ID when selected
// - ignores unsubscribed host ID
// - verifies host has a configured TargetPool in HostConfig
func TestSubscriptionMatcher(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// host-1: TargetPool configured
	if err := st.SetHostConfig(HostConfig{HostID: "host-1", TargetPool: "pool-a"}); err != nil {
		t.Fatalf("SetHostConfig host-1: %v", err)
	}
	// host-2: TargetPool configured
	if err := st.SetHostConfig(HostConfig{HostID: "host-2", TargetPool: "pool-b"}); err != nil {
		t.Fatalf("SetHostConfig host-2: %v", err)
	}
	// host-3: TargetPool configured
	if err := st.SetHostConfig(HostConfig{HostID: "host-3", TargetPool: "pool-c"}); err != nil {
		t.Fatalf("SetHostConfig host-3: %v", err)
	}
	// host-no-pool: TargetPool empty
	if err := st.SetHostConfig(HostConfig{HostID: "host-no-pool", TargetPool: ""}); err != nil {
		t.Fatalf("SetHostConfig host-no-pool: %v", err)
	}
	// host-unknown: not in HostConfigs

	sm := NewSyncManager(st, nil, nil, nil, nil, nil)

	// Mode "all"
	itemAll := Item{
		ID:         "item-1",
		Name:       "debian-all",
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	}
	if !sm.IsSubscribed("host-1", itemAll) {
		t.Errorf("expected host-1 to match mode 'all'")
	}
	if !sm.IsSubscribed("host-2", itemAll) {
		t.Errorf("expected host-2 to match mode 'all'")
	}
	if sm.IsSubscribed("host-no-pool", itemAll) {
		t.Errorf("expected host-no-pool to NOT match because TargetPool is empty")
	}
	if sm.IsSubscribed("host-unknown", itemAll) {
		t.Errorf("expected host-unknown to NOT match because no HostConfig exists")
	}

	// Mode "selected" with specific host IDs
	itemSelected := Item{
		ID:         "item-2",
		Name:       "ubuntu-selected",
		SyncPolicy: SyncPolicy{Mode: SyncSelected, Hosts: []string{"host-1", "host-3"}},
	}
	if !sm.IsSubscribed("host-1", itemSelected) {
		t.Errorf("expected host-1 to match selected hosts")
	}
	if !sm.IsSubscribed("host-3", itemSelected) {
		t.Errorf("expected host-3 to match selected hosts")
	}
	if sm.IsSubscribed("host-2", itemSelected) {
		t.Errorf("expected host-2 to be unsubscribed / ignored")
	}

	// Mode "manual"
	itemManual := Item{
		ID:         "item-3",
		Name:       "rocky-manual",
		SyncPolicy: SyncPolicy{Mode: SyncManual},
	}
	if sm.IsSubscribed("host-1", itemManual) {
		t.Errorf("expected host-1 to NOT be subscribed for mode 'manual'")
	}
}

// TestAutoSyncTrigger verifies that calling OnItemUpdated on a subscribed host spawns
// a content-sync job in jobs.Manager.
func TestAutoSyncTrigger(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-local"
	sourceHost := "host-remote"

	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	payload := []byte("auto-sync-test-payload-data")
	payloadSHA := sha256Hex(payload)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	peerResolver := MapPeerResolver{
		sourceHost: peer,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, peerResolver, nil)

	item := Item{
		ID:         "item-ubuntu",
		Name:       "ubuntu-server",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			{
				ID:         "v2404",
				Version:    "24.04",
				Format:     "qcow2",
				Size:       int64(len(payload)),
				SHA256:     payloadSHA,
				SourceHost: sourceHost,
			},
		},
	}

	// Call OnItemUpdated
	sm.OnItemUpdated(ctx, item, selfID)

	// Verify job was spawned in jobs.Manager
	jobList := jm.List()
	if len(jobList) == 0 {
		t.Fatalf("expected a content-sync job to be spawned in jobs.Manager, got 0")
	}

	var foundJob *jobs.Job
	for i := range jobList {
		if jobList[i].Kind == "content-sync" {
			foundJob = &jobList[i]
			break
		}
	}
	if foundJob == nil {
		t.Fatalf("expected job with kind 'content-sync', found: %+v", jobList)
	}

	expectedTitle := "Sync ubuntu-server (v24.04)"
	if foundJob.Title != expectedTitle {
		t.Errorf("job.Title = %q, want %q", foundJob.Title, expectedTitle)
	}

	// Wait for job to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := jm.Get(foundJob.ID)
		if ok && j.State != jobs.Running {
			if j.State != jobs.Succeeded {
				t.Fatalf("job failed: %s", j.Message)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify downloaded file is now in target directory
	expectedFile := filepath.Join(targetDir, "content-ubuntu-server-v24.04.qcow2")
	if st, err := os.Stat(expectedFile); err != nil || st.Size() != int64(len(payload)) {
		t.Errorf("expected downloaded file %s with size %d, err: %v", expectedFile, len(payload), err)
	}
}

// TestRetentionCleanup verifies that when a new version syncs and exceeds KeepVersions,
// the pruned older version's volume file is deleted from disk.
func TestRetentionCleanup(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-alpha"
	sourceHost := "host-beta"

	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	itemName := "alpine"
	// Pre-create local files for v1.0 and v2.0
	fileV1 := filepath.Join(targetDir, "content-alpine-v1.0.qcow2")
	fileV2 := filepath.Join(targetDir, "content-alpine-v2.0.qcow2")
	if err := os.WriteFile(fileV1, []byte("v1-content"), 0644); err != nil {
		t.Fatalf("write v1 file: %v", err)
	}
	if err := os.WriteFile(fileV2, []byte("v2-content"), 0644); err != nil {
		t.Fatalf("write v2 file: %v", err)
	}

	payloadV3 := []byte("v3-newest-image-content")
	payloadV3SHA := sha256Hex(payloadV3)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payloadV3)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payloadV3)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	peerResolver := MapPeerResolver{
		sourceHost: peer,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, peerResolver, nil)

	// Hook to verify pool refresh was triggered
	refreshedPool := ""
	sm.SetRefreshPoolHook(func(poolName string) error {
		refreshedPool = poolName
		return nil
	})

	// KeepVersions = 2, but Versions contains 3 versions (v1, v2, v3).
	// Therefore v1 is in excess and must be pruned upon syncing v3.
	item := Item{
		ID:           "item-alpine",
		Name:         itemName,
		Type:         TypeImage,
		KeepVersions: 2,
		SyncPolicy:   SyncPolicy{Mode: SyncAll},
		Versions: []ItemVersion{
			{
				ID:         "v1",
				Version:    "1.0",
				Format:     "qcow2",
				Size:       int64(len("v1-content")),
				SHA256:     sha256Hex([]byte("v1-content")),
				SourceHost: sourceHost,
			},
			{
				ID:         "v2",
				Version:    "2.0",
				Format:     "qcow2",
				Size:       int64(len("v2-content")),
				SHA256:     sha256Hex([]byte("v2-content")),
				SourceHost: sourceHost,
			},
			{
				ID:         "v3",
				Version:    "3.0",
				Format:     "qcow2",
				Size:       int64(len(payloadV3)),
				SHA256:     payloadV3SHA,
				SourceHost: sourceHost,
			},
		},
	}

	sm.OnItemUpdated(ctx, item, selfID)

	// Wait for the sync job to complete
	deadline := time.Now().Add(5 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		jobList := jm.List()
		if len(jobList) > 0 && jobList[0].State != jobs.Running {
			if jobList[0].State != jobs.Succeeded {
				t.Fatalf("sync job failed: %s", jobList[0].Message)
			}
			completed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !completed {
		t.Fatalf("sync job timed out")
	}

	// 1. Older pruned version (v1.0) must be deleted from disk
	if _, err := os.Stat(fileV1); !os.IsNotExist(err) {
		t.Errorf("expected pruned file %s to be deleted, but it still exists", fileV1)
	}

	// 2. Kept version (v2.0) must still exist
	if _, err := os.Stat(fileV2); err != nil {
		t.Errorf("expected kept file %s to exist, err: %v", fileV2, err)
	}

	// 3. New version (v3.0) must exist
	fileV3 := filepath.Join(targetDir, "content-alpine-v3.0.qcow2")
	if st, err := os.Stat(fileV3); err != nil || st.Size() != int64(len(payloadV3)) {
		t.Errorf("expected synced file %s with size %d, err: %v", fileV3, len(payloadV3), err)
	}

	// 4. Refresh pool hook was called
	if refreshedPool != targetDir {
		t.Errorf("refreshedPool = %q, want %q", refreshedPool, targetDir)
	}
}

// TestReplicateToMembers verifies that ReplicateToMembers calls all mock peers with ?local=1
// and records failed hosts.
func TestReplicateToMembers(t *testing.T) {
	clusterPath := filepath.Join(t.TempDir(), "cluster.json")
	cl, err := cluster.Open(clusterPath, "node-1", "fp-node-1")
	if err != nil {
		t.Fatalf("cluster.Open: %v", err)
	}

	var peerACalled atomic.Bool
	var peerAPath atomic.Value

	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerACalled.Store(true)
		peerAPath.Store(r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer serverA.Close()

	// serverB returns 500 error to simulate member failure
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer serverB.Close()

	nodeSelf := cl.Self("https://10.0.0.1:8420")
	nodeA := cluster.Node{ID: "node-a", Name: "member-a", Address: serverA.URL}
	nodeB := cluster.Node{ID: "node-b", Name: "member-b", Address: serverB.URL}

	if err := cl.Replace([]cluster.Node{nodeSelf, nodeA, nodeB}, 1); err != nil {
		t.Fatalf("cl.Replace: %v", err)
	}
	if !cl.Clustered() {
		t.Fatalf("expected cluster to be clustered")
	}

	peerA := cluster.NewPeer(nodeA, serverA.Client())
	peerB := cluster.NewPeer(nodeB, serverB.Client())

	resolver := MapPeerResolver{
		"node-a": peerA,
		"node-b": peerB,
	}

	ctx := context.Background()
	body := map[string]string{"name": "test-item"}

	// 1. Standard replication: node-a succeeds with ?local=1, node-b fails
	failed := ReplicateToMembers(ctx, cl, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items", body)

	if !peerACalled.Load() {
		t.Errorf("expected peer A to be called")
	}
	pathCalled, _ := peerAPath.Load().(string)
	if !strings.HasSuffix(pathCalled, "?local=1") {
		t.Errorf("expected peer A to be called with ?local=1, got: %s", pathCalled)
	}

	if !slices.Contains(failed, "member-b") {
		t.Errorf("expected failed members to contain 'member-b', got: %v", failed)
	}
	if slices.Contains(failed, "member-a") {
		t.Errorf("did not expect successful member-a in failed list: %v", failed)
	}

	// 2. Unreachable / unresolvable member
	nodeC := cluster.Node{ID: "node-c", Name: "member-c", Address: "https://invalid"}
	_ = cl.Replace([]cluster.Node{nodeSelf, nodeA, nodeC}, 2)
	failedWithMissing := ReplicateToMembers(ctx, cl, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items", body)
	if !slices.Contains(failedWithMissing, "member-c") {
		t.Errorf("expected unresolvable member-c to be in failed list, got: %v", failedWithMissing)
	}

	// 3. Skip replication when ?local=1 is already set
	peerACalled.Store(false)
	skipRes := ReplicateToMembers(ctx, cl, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items?local=1", body)
	if skipRes != nil {
		t.Errorf("expected nil result when ?local=1 is present, got: %v", skipRes)
	}
	if peerACalled.Load() {
		t.Errorf("expected peer A NOT to be called when ?local=1 is already set")
	}

	// 4. Skip replication when standalone
	alonePath := filepath.Join(t.TempDir(), "alone.json")
	clAlone, _ := cluster.Open(alonePath, "node-alone", "fp-alone")
	aloneRes := ReplicateToMembers(ctx, clAlone, resolver, "admin", "admin", "token-123", http.MethodPost, "/api/v1/content/items", body)
	if aloneRes != nil {
		t.Errorf("expected nil result when standalone, got: %v", aloneRes)
	}
}

// TestReconcile verifies that Reconcile sweeps items and triggers OnItemUpdated only for
// subscribed items missing the newest version locally, without starting duplicate jobs.
func TestReconcile(t *testing.T) {
	st, err := Open("")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	targetDir := t.TempDir()
	selfID := "host-1"
	sourceHost := "host-2"

	if err := st.SetHostConfig(HostConfig{HostID: selfID, TargetPool: targetDir}); err != nil {
		t.Fatalf("SetHostConfig: %v", err)
	}

	payloadMissing := []byte("missing-payload")
	payloadPresent := []byte("already-present-payload")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payloadMissing)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payloadMissing)
	}))
	defer server.Close()

	peer := cluster.NewPeer(cluster.Node{ID: sourceHost, Address: server.URL}, server.Client())
	peerResolver := MapPeerResolver{
		sourceHost: peer,
	}

	// Item 1: subscribed and missing locally
	itemMissing, err := st.CreateItem(Item{
		Name:       "missing-item",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	})
	if err != nil {
		t.Fatalf("CreateItem missing: %v", err)
	}
	itemMissing, _, err = st.AddVersion(itemMissing.ID, ItemVersion{
		ID:         "v1",
		Version:    "1.0",
		Format:     "qcow2",
		Size:       int64(len(payloadMissing)),
		SHA256:     sha256Hex(payloadMissing),
		SourceHost: sourceHost,
	})
	if err != nil {
		t.Fatalf("AddVersion missing: %v", err)
	}

	// Item 2: subscribed but already present locally
	itemPresent, err := st.CreateItem(Item{
		Name:       "present-item",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncAll},
	})
	if err != nil {
		t.Fatalf("CreateItem present: %v", err)
	}
	itemPresent, _, err = st.AddVersion(itemPresent.ID, ItemVersion{
		ID:         "v1",
		Version:    "1.0",
		Format:     "qcow2",
		Size:       int64(len(payloadPresent)),
		SHA256:     sha256Hex(payloadPresent),
		SourceHost: sourceHost,
	})
	if err != nil {
		t.Fatalf("AddVersion present: %v", err)
	}
	presentPath := filepath.Join(targetDir, "content-present-item-v1.0.qcow2")
	if err := os.WriteFile(presentPath, payloadPresent, 0644); err != nil {
		t.Fatalf("write present file: %v", err)
	}

	// Item 3: unsubscribed (manual mode)
	itemUnsubscribed, err := st.CreateItem(Item{
		Name:       "unsub-item",
		Type:       TypeImage,
		SyncPolicy: SyncPolicy{Mode: SyncManual},
	})
	if err != nil {
		t.Fatalf("CreateItem unsub: %v", err)
	}
	_, _, err = st.AddVersion(itemUnsubscribed.ID, ItemVersion{
		ID:         "v1",
		Version:    "1.0",
		Format:     "qcow2",
		Size:       int64(len(payloadMissing)),
		SHA256:     sha256Hex(payloadMissing),
		SourceHost: sourceHost,
	})
	if err != nil {
		t.Fatalf("AddVersion unsub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jm := jobs.New(ctx)
	sm := NewSyncManager(st, jm, nil, nil, peerResolver, nil)

	// Run Reconcile
	sm.Reconcile(ctx, selfID)

	// Only missing-item should spawn a job
	jobList := jm.List()
	if len(jobList) != 1 {
		t.Fatalf("expected exactly 1 job spawned by Reconcile, got %d: %+v", len(jobList), jobList)
	}
	if !strings.Contains(jobList[0].Title, "missing-item") {
		t.Errorf("expected job for missing-item, got title: %s", jobList[0].Title)
	}

	// Run Reconcile again while job is running -> activeJobs prevents duplicate job
	sm.Reconcile(ctx, selfID)
	if len(jm.List()) != 1 {
		t.Errorf("expected duplicate job to be prevented by activeJobs, got %d jobs", len(jm.List()))
	}
}
