package content

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "content.json"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	return s
}

func TestStorePersistenceAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.json")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open s1 failed: %v", err)
	}

	it, err := s1.CreateItem(Item{
		ID:           "debian-13-template",
		Name:         "Debian 13 Cloud",
		Type:         TypeTemplate,
		Description:  "Base template for Debian 13",
		SourceHost:   "host-01",
		KeepVersions: 4,
		SyncPolicy: SyncPolicy{
			Mode:  SyncSelected,
			Hosts: []string{"host-02", "host-03"},
		},
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	v1 := ItemVersion{
		ID:          "v1-initial",
		Version:     "1.0.0",
		Description: "Initial release",
		SHA256:      testSHA256,
		Size:        1024 * 1024 * 500,
		Format:      "qcow2",
		SourceHost:  "host-01",
		SourcePool:  "default",
		SourceVol:   "debian13-v1.qcow2",
		TemplateXML: "<domain type='kvm'><name>deb13</name></domain>",
	}

	updated, pruned, err := s1.AddVersion(it.ID, v1)
	if err != nil {
		t.Fatalf("AddVersion failed: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("unexpected pruned versions: %v", pruned)
	}
	if len(updated.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(updated.Versions))
	}

	if err := s1.SetHostConfig(HostConfig{
		HostID:     "host-02",
		TargetPool: "images",
		AutoSync:   true,
	}); err != nil {
		t.Fatalf("SetHostConfig failed: %v", err)
	}

	// Re-open store from disk
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open s2 failed: %v", err)
	}

	gotItem, ok := s2.GetItem("debian-13-template")
	if !ok {
		t.Fatal("expected item to be found in re-opened store")
	}
	if gotItem.Name != "Debian 13 Cloud" || gotItem.Type != TypeTemplate || gotItem.KeepVersions != 4 {
		t.Fatalf("item fields mismatch: %+v", gotItem)
	}
	if gotItem.SyncPolicy.Mode != SyncSelected || len(gotItem.SyncPolicy.Hosts) != 2 {
		t.Fatalf("sync policy mismatch: %+v", gotItem.SyncPolicy)
	}
	if len(gotItem.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(gotItem.Versions))
	}
	gotVer := gotItem.Versions[0]
	if gotVer.ID != "v1-initial" || gotVer.SHA256 != testSHA256 || gotVer.TemplateXML != v1.TemplateXML {
		t.Fatalf("version fields mismatch: %+v", gotVer)
	}

	hc, ok := s2.GetHostConfig("host-02")
	if !ok {
		t.Fatal("expected host config to be found in re-opened store")
	}
	if hc.TargetPool != "images" || !hc.AutoSync {
		t.Fatalf("host config mismatch: %+v", hc)
	}
}

func TestCreateItemValidation(t *testing.T) {
	s := testStore(t)

	// 1. Empty name
	_, err := s.CreateItem(Item{
		Name: "   ",
		Type: TypeISO,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty name, got: %v", err)
	}

	// 2. Bad type
	_, err = s.CreateItem(Item{
		Name: "Ubuntu Server",
		Type: "unknown-type",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad type, got: %v", err)
	}

	// 3. Valid creation with default KeepVersions and generated ID
	item1, err := s.CreateItem(Item{
		Name: "Ubuntu 24.04 ISO",
		Type: TypeISO,
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}
	if item1.ID == "" || len(item1.ID) != 16 {
		t.Fatalf("expected 16-hex generated ID, got %q", item1.ID)
	}
	if item1.KeepVersions != 3 {
		t.Fatalf("expected default KeepVersions = 3, got %d", item1.KeepVersions)
	}

	// 4. Duplicate name (case-insensitive)
	_, err = s.CreateItem(Item{
		Name: "ubuntu 24.04 iso",
		Type: TypeISO,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for duplicate name, got: %v", err)
	}

	// 5. Duplicate ID
	_, err = s.CreateItem(Item{
		ID:   item1.ID,
		Name: "Different Name",
		Type: TypeImage,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for duplicate ID, got: %v", err)
	}

	// 6. Invalid ID characters
	_, err = s.CreateItem(Item{
		ID:   "invalid id with spaces",
		Name: "Valid Name",
		Type: TypeImage,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for invalid ID format, got: %v", err)
	}

	// 7. Invalid SyncMode
	_, err = s.CreateItem(Item{
		Name: "Bad Sync",
		Type: TypeImage,
		SyncPolicy: SyncPolicy{
			Mode: "bogus",
		},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bogus SyncMode, got: %v", err)
	}
}

func TestAddVersionRetentionPruning(t *testing.T) {
	s := testStore(t)

	item, err := s.CreateItem(Item{
		ID:           "test-item",
		Name:         "Alpine Image",
		Type:         TypeImage,
		KeepVersions: 2,
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	// Add v1
	item, pruned, err := s.AddVersion(item.ID, ItemVersion{
		ID:      "v1",
		Version: "1.0",
		SHA256:  testSHA256,
		Size:    100,
	})
	if err != nil {
		t.Fatalf("AddVersion v1 failed: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("expected no pruned versions on v1, got %v", pruned)
	}
	if len(item.Versions) != 1 {
		t.Fatalf("expected 1 version, got %d", len(item.Versions))
	}

	// Add v2
	item, pruned, err = s.AddVersion(item.ID, ItemVersion{
		ID:      "v2",
		Version: "2.0",
		SHA256:  testSHA256,
		Size:    200,
	})
	if err != nil {
		t.Fatalf("AddVersion v2 failed: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("expected no pruned versions on v2, got %v", pruned)
	}
	if len(item.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(item.Versions))
	}

	// Add v3 -> should prune v1 since KeepVersions is 2
	item, pruned, err = s.AddVersion(item.ID, ItemVersion{
		ID:      "v3",
		Version: "3.0",
		SHA256:  testSHA256,
		Size:    300,
	})
	if err != nil {
		t.Fatalf("AddVersion v3 failed: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "v1" {
		t.Fatalf("expected pruned [v1], got %v", pruned)
	}
	if len(item.Versions) != 2 {
		t.Fatalf("expected 2 versions left, got %d", len(item.Versions))
	}
	if item.Versions[0].ID != "v2" || item.Versions[1].ID != "v3" {
		t.Fatalf("expected [v2, v3], got [%s, %s]", item.Versions[0].ID, item.Versions[1].ID)
	}

	// Verify persistence has pruned versions too
	persisted, ok := s.GetItem(item.ID)
	if !ok {
		t.Fatal("item not found")
	}
	if len(persisted.Versions) != 2 {
		t.Fatalf("expected persisted 2 versions, got %d", len(persisted.Versions))
	}

	// AddVersion input validation tests:
	// a. Empty version
	_, _, err = s.AddVersion(item.ID, ItemVersion{
		Version: "  ",
		SHA256:  testSHA256,
		Size:    100,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty version, got: %v", err)
	}

	// b. Bad SHA256 (not 64 lowercase hex)
	badSHAs := []string{
		"12345",
		strings.Repeat("G", 64),
		strings.ToUpper(testSHA256), // must be lowercase
		testSHA256[:63],
	}
	for _, bad := range badSHAs {
		_, _, err = s.AddVersion(item.ID, ItemVersion{
			Version: "4.0",
			SHA256:  bad,
			Size:    100,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected ErrInvalid for bad SHA %q, got: %v", bad, err)
		}
	}

	// c. Size <= 0
	_, _, err = s.AddVersion(item.ID, ItemVersion{
		Version: "4.0",
		SHA256:  testSHA256,
		Size:    0,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for size 0, got: %v", err)
	}

	// d. Non-existent item
	_, _, err = s.AddVersion("no-such-item", ItemVersion{
		Version: "1.0",
		SHA256:  testSHA256,
		Size:    100,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent item, got: %v", err)
	}
}

func TestDeleteVersionRestrictions(t *testing.T) {
	s := testStore(t)

	item, err := s.CreateItem(Item{
		Name: "Single Version Item",
		Type: TypeTemplate,
	})
	if err != nil {
		t.Fatalf("CreateItem failed: %v", err)
	}

	item, _, err = s.AddVersion(item.ID, ItemVersion{
		ID:      "v1",
		Version: "1.0",
		SHA256:  testSHA256,
		Size:    100,
	})
	if err != nil {
		t.Fatalf("AddVersion failed: %v", err)
	}

	// Attempting to delete the only version must be refused
	err = s.DeleteVersion(item.ID, "v1")
	if err == nil {
		t.Fatal("expected DeleteVersion on only version to fail, got nil")
	}
	if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrConflict or ErrInvalid, got: %v", err)
	}

	// Add second version
	item, _, err = s.AddVersion(item.ID, ItemVersion{
		ID:      "v2",
		Version: "2.0",
		SHA256:  testSHA256,
		Size:    200,
	})
	if err != nil {
		t.Fatalf("AddVersion v2 failed: %v", err)
	}

	// Now deleting v1 should succeed
	if err := s.DeleteVersion(item.ID, "v1"); err != nil {
		t.Fatalf("DeleteVersion v1 failed: %v", err)
	}

	updated, ok := s.GetItem(item.ID)
	if !ok {
		t.Fatal("item not found")
	}
	if len(updated.Versions) != 1 || updated.Versions[0].ID != "v2" {
		t.Fatalf("expected only v2 to remain, got %+v", updated.Versions)
	}

	// Delete non-existent version returns ErrNotFound
	err = s.DeleteVersion(item.ID, "non-existent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent version, got %v", err)
	}

	// Delete version from non-existent item returns ErrNotFound
	err = s.DeleteVersion("non-existent-item", "v2")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent item, got %v", err)
	}
}

func TestHostConfigSavingAndRetrieval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.json")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Non-existent host
	_, ok := s.GetHostConfig("host-a")
	if ok {
		t.Fatal("expected GetHostConfig on missing host to return false")
	}

	// Empty host ID returns ErrInvalid
	err = s.SetHostConfig(HostConfig{HostID: "  "})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty hostId, got: %v", err)
	}

	// Set valid configs
	if err := s.SetHostConfig(HostConfig{
		HostID:     "host-a",
		TargetPool: "pool-a",
		AutoSync:   true,
	}); err != nil {
		t.Fatalf("SetHostConfig host-a failed: %v", err)
	}

	if err := s.SetHostConfig(HostConfig{
		HostID:     "host-b",
		TargetPool: "pool-b",
		AutoSync:   false,
	}); err != nil {
		t.Fatalf("SetHostConfig host-b failed: %v", err)
	}

	// Retrieve
	cfgA, ok := s.GetHostConfig("host-a")
	if !ok || cfgA.TargetPool != "pool-a" || !cfgA.AutoSync {
		t.Fatalf("unexpected config for host-a: %+v", cfgA)
	}

	all := s.ListHostConfigs()
	if len(all) != 2 {
		t.Fatalf("expected 2 host configs, got %d", len(all))
	}
	if all[0].HostID != "host-a" || all[1].HostID != "host-b" {
		t.Fatalf("expected sorted host configs, got %+v", all)
	}

	// Re-open and verify persistence
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open s2 failed: %v", err)
	}
	cfgB, ok := s2.GetHostConfig("host-b")
	if !ok || cfgB.TargetPool != "pool-b" || cfgB.AutoSync {
		t.Fatalf("unexpected config for host-b across re-open: %+v", cfgB)
	}
}

func TestCorruptJSONHandling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.json")

	if err := os.WriteFile(path, []byte("{ this is corrupt json !!!"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := Open(path)
	if err == nil {
		t.Fatal("expected Open on corrupt JSON to fail, got nil error")
	}
}

func TestMissingFileAndInMemory(t *testing.T) {
	// 1. Missing file returns empty store
	s, err := Open(filepath.Join(t.TempDir(), "non-existent.json"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if items := s.ListItems(); len(items) != 0 {
		t.Fatalf("expected empty items, got %d", len(items))
	}

	// 2. Empty path returns in-memory store (save does not error)
	mem, err := Open("")
	if err != nil {
		t.Fatalf("Open with empty path failed: %v", err)
	}
	it, err := mem.CreateItem(Item{Name: "Memory", Type: TypeISO})
	if err != nil {
		t.Fatalf("CreateItem in-memory failed: %v", err)
	}
	if _, ok := mem.GetItem(it.ID); !ok {
		t.Fatal("item not found in memory store")
	}
}

func TestStoreUpdateAndDelete(t *testing.T) {
	s := testStore(t)

	it1, err := s.CreateItem(Item{
		Name:         "Item 1",
		Type:         TypeImage,
		Description:  "Initial desc",
		KeepVersions: 2,
	})
	if err != nil {
		t.Fatalf("CreateItem 1 failed: %v", err)
	}

	it2, err := s.CreateItem(Item{
		Name: "Item 2",
		Type: TypeISO,
	})
	if err != nil {
		t.Fatalf("CreateItem 2 failed: %v", err)
	}

	// List items sorted by name
	list := s.ListItems()
	if len(list) != 2 || list[0].Name != "Item 1" || list[1].Name != "Item 2" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Update item
	it1.Description = "Updated desc"
	it1.KeepVersions = 5
	it1.SyncPolicy = SyncPolicy{Mode: SyncAll}
	updated, err := s.UpdateItem(it1)
	if err != nil {
		t.Fatalf("UpdateItem failed: %v", err)
	}
	if updated.Description != "Updated desc" || updated.KeepVersions != 5 || updated.SyncPolicy.Mode != SyncAll {
		t.Fatalf("unexpected updated item: %+v", updated)
	}

	// Update with duplicate name of another item
	it1.Name = "Item 2"
	_, err = s.UpdateItem(it1)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for duplicate name on update, got: %v", err)
	}

	// Update with empty name
	it1.Name = "   "
	_, err = s.UpdateItem(it1)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty name on update, got: %v", err)
	}

	// Update non-existent item
	_, err = s.UpdateItem(Item{ID: "missing", Name: "Name"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing item on update, got: %v", err)
	}

	// Delete item
	if err := s.DeleteItem(it2.ID); err != nil {
		t.Fatalf("DeleteItem failed: %v", err)
	}
	if _, ok := s.GetItem(it2.ID); ok {
		t.Fatal("expected deleted item to be missing")
	}

	// Delete non-existent item
	err = s.DeleteItem("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing item on delete, got: %v", err)
	}
}
