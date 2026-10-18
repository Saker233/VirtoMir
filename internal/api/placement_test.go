package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/cluster"
	"github.com/onurbaydas/virtomir/internal/drs"
	"github.com/onurbaydas/virtomir/internal/live"
	"github.com/onurbaydas/virtomir/internal/placement"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func newTestPlacementServer(t *testing.T, b Backend, lv Live) (*Server, *placement.Store, string) {
	t.Helper()
	dir := t.TempDir()
	cert, err := auth.SelfSignedCert(filepath.Join(dir, "tls"))
	if err != nil {
		t.Fatal(err)
	}
	fp := cluster.Fingerprint(cert.Certificate[0])
	clStore, err := cluster.Open(filepath.Join(dir, "cluster.json"), "local", fp)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := placement.Open(filepath.Join(dir, "placement.json"))
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		b = &fakeBackend{}
	}
	if lv == nil {
		lv = &fakeLive{}
	}
	srv := NewServer(Options{
		Backend:   b,
		Live:      lv,
		Placement: pl,
		Cluster:   clStore,
		HostName:  "local",
	})
	return srv, pl, clStore.SelfID()
}

func serveReq(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// 1. Rule CRUD round trip
func TestPlacementRuleCRUD(t *testing.T) {
	srv, _, _ := newTestPlacementServer(t, nil, nil)

	// Create
	createBody := `{"name":"web-collocate","kind":"vm_affinity","enabled":true,"strength":"must","domains":["vm-1","vm-2"]}`
	rec := serveReq(srv, http.MethodPost, "/api/v1/placement/rules", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create rule: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var created placement.Rule
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created rule: %v", err)
	}
	if created.ID == "" || len(created.ID) != 16 {
		t.Fatalf("expected 16 hex digit ID, got %q", created.ID)
	}
	if created.Name != "web-collocate" || created.Kind != placement.VMAffinity {
		t.Fatalf("unexpected created rule: %+v", created)
	}

	// Read (GET /api/v1/placement)
	rec = serveReq(srv, http.MethodGet, "/api/v1/placement", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get placement: status %d", rec.Code)
	}
	var getResp struct {
		Rules      []placement.Rule             `json:"rules"`
		HostLabels map[string]map[string]string `json:"hostLabels"`
		Violations []placement.Violation        `json:"violations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal get placement: %v", err)
	}
	if len(getResp.Rules) != 1 || getResp.Rules[0].ID != created.ID {
		t.Fatalf("expected 1 rule with ID %s, got %+v", created.ID, getResp.Rules)
	}

	// Update (PUT /api/v1/placement/rules/{id})
	updateBody := `{"name":"web-collocate-soft","kind":"vm_affinity","enabled":true,"strength":"should","domains":["vm-1","vm-2"]}`
	rec = serveReq(srv, http.MethodPut, "/api/v1/placement/rules/"+created.ID, updateBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("update rule: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var updated placement.Rule
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal updated rule: %v", err)
	}
	if updated.Name != "web-collocate-soft" || updated.Strength != placement.Should {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}

	// Delete (DELETE /api/v1/placement/rules/{id})
	rec = serveReq(srv, http.MethodDelete, "/api/v1/placement/rules/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete rule: status %d, body: %s", rec.Code, rec.Body.String())
	}

	// Read again: should be empty
	rec = serveReq(srv, http.MethodGet, "/api/v1/placement", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get placement after delete: status %d", rec.Code)
	}
	var getResp2 struct {
		Rules []placement.Rule `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &getResp2); err != nil {
		t.Fatal(err)
	}
	if len(getResp2.Rules) != 0 {
		t.Fatalf("expected 0 rules after delete, got %d", len(getResp2.Rules))
	}
}

// 2. 422 on a conflicting rule and on a vm_host rule without hosts
func TestPlacementValidationErrors(t *testing.T) {
	srv, _, _ := newTestPlacementServer(t, nil, nil)

	// Create valid affinity rule between vm-1 and vm-2
	rule1 := `{"name":"aff-1","kind":"vm_affinity","enabled":true,"strength":"must","domains":["vm-1","vm-2"]}`
	rec := serveReq(srv, http.MethodPost, "/api/v1/placement/rules", rule1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create rule1: status %d", rec.Code)
	}

	// Conflict: try to create must anti-affinity rule between vm-1 and vm-2
	conflictRule := `{"name":"anti-1","kind":"vm_anti_affinity","enabled":true,"strength":"must","domains":["vm-1","vm-2"]}`
	rec = serveReq(srv, http.MethodPost, "/api/v1/placement/rules", conflictRule)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on conflicting rule, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// vm_host rule without hosts
	noHostsRule := `{"name":"host-rule","kind":"vm_host","enabled":true,"strength":"must","domains":["vm-1"],"hosts":[]}`
	rec = serveReq(srv, http.MethodPost, "/api/v1/placement/rules", noHostsRule)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 on vm_host rule without hosts, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// 3. Labels round trip and 404/422 for an unknown host id
func TestPlacementHostLabels(t *testing.T) {
	srv, _, hostID := newTestPlacementServer(t, nil, nil)

	// Round trip on known host (both hostID and "local" are known)
	labelBody := `{"labels":{"rack":"r1","zone":"us-east-1"}}`
	rec := serveReq(srv, http.MethodPut, "/api/v1/placement/hosts/"+hostID+"/labels", labelBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("set labels: status %d, body: %s", rec.Code, rec.Body.String())
	}

	rec = serveReq(srv, http.MethodGet, "/api/v1/placement", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get placement: status %d", rec.Code)
	}
	var getResp struct {
		HostLabels map[string]map[string]string `json:"hostLabels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &getResp); err != nil {
		t.Fatal(err)
	}
	if getResp.HostLabels[hostID]["rack"] != "r1" || getResp.HostLabels[hostID]["zone"] != "us-east-1" {
		t.Fatalf("unexpected host labels: %+v", getResp.HostLabels)
	}

	// 404 for unknown host id
	rec = serveReq(srv, http.MethodPut, "/api/v1/placement/hosts/nonexistent-host/labels", labelBody)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown host id, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 422 for invalid label key
	badLabelBody := `{"labels":{"INVALID KEY!":"value"}}`
	rec = serveReq(srv, http.MethodPut, "/api/v1/placement/hosts/"+hostID+"/labels", badLabelBody)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid label key, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// 4. Check endpoint returns hard for a must anti-affinity collision and soft for a should rule
func TestPlacementCheck(t *testing.T) {
	// Seed live snapshot: vm-1 and vm-2 running on host
	snap := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-1", Name: "vm-1", State: virt.StateRunning}},
			{Domain: virt.Domain{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning}},
		},
	}
	rawSnap, _ := json.Marshal(snap)
	lv := &fakeLive{backlog: [][]byte{rawSnap}}
	srv, pl, hostID := newTestPlacementServer(t, nil, lv)

	// Must anti-affinity rule between vm-1 and vm-2
	_, err := pl.AddRule(placement.Rule{
		Name:     "Separate Web",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should vm_host rule: vm-3 should not be on hostID
	_, err = pl.AddRule(placement.Rule{
		Name:     "Prefer Off Host",
		Kind:     placement.VMHost,
		Enabled:  true,
		Strength: placement.Should,
		Domains:  []string{"vm-3"},
		Hosts:    []string{hostID},
		Exclude:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check placing vm-1 on hostID (vm-2 is already running there -> hard anti-affinity collision)
	checkBodyHard := fmt.Sprintf(`{"domainUuid":"vm-1","hostId":"%s"}`, hostID)
	rec := serveReq(srv, http.MethodPost, "/api/v1/placement/check", checkBodyHard)
	if rec.Code != http.StatusOK {
		t.Fatalf("check hard: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var checkResHard struct {
		OK   bool     `json:"ok"`
		Hard []string `json:"hard"`
		Soft []string `json:"soft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &checkResHard); err != nil {
		t.Fatal(err)
	}
	if checkResHard.OK || len(checkResHard.Hard) == 0 {
		t.Fatalf("expected Hard violation and OK=false, got: %+v", checkResHard)
	}
	if !strings.Contains(checkResHard.Hard[0], "Separate Web") {
		t.Errorf("expected rule name in message, got %s", checkResHard.Hard[0])
	}

	// Check placing vm-3 on hostID (violates should vm_host rule -> soft collision)
	checkBodySoft := fmt.Sprintf(`{"domainUuid":"vm-3","hostId":"%s"}`, hostID)
	rec = serveReq(srv, http.MethodPost, "/api/v1/placement/check", checkBodySoft)
	if rec.Code != http.StatusOK {
		t.Fatalf("check soft: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var checkResSoft struct {
		OK   bool     `json:"ok"`
		Hard []string `json:"hard"`
		Soft []string `json:"soft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &checkResSoft); err != nil {
		t.Fatal(err)
	}
	if !checkResSoft.OK || len(checkResSoft.Soft) == 0 || len(checkResSoft.Hard) != 0 {
		t.Fatalf("expected Soft violation and OK=true, got: %+v", checkResSoft)
	}
	if !strings.Contains(checkResSoft.Soft[0], "Prefer Off Host") {
		t.Errorf("expected rule name in soft message, got %s", checkResSoft.Soft[0])
	}

	// Check with empty domainUuid (not-yet-created machine) -> clean OK
	checkBodyEmpty := fmt.Sprintf(`{"domainUuid":"","hostId":"%s"}`, hostID)
	rec = serveReq(srv, http.MethodPost, "/api/v1/placement/check", checkBodyEmpty)
	if rec.Code != http.StatusOK {
		t.Fatalf("check empty: status %d", rec.Code)
	}
	var checkResEmpty struct {
		OK   bool     `json:"ok"`
		Hard []string `json:"hard"`
		Soft []string `json:"soft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &checkResEmpty); err != nil {
		t.Fatal(err)
	}
	if !checkResEmpty.OK || len(checkResEmpty.Hard) != 0 || len(checkResEmpty.Soft) != 0 {
		t.Fatalf("expected clean ok for empty domainUuid, got %+v", checkResEmpty)
	}
}

// 5. Violations endpoint lists a seeded collision
func TestPlacementViolations(t *testing.T) {
	// Seed live snapshot: vm-1 and vm-2 both running on host
	snap := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-1", Name: "vm-1", State: virt.StateRunning}},
			{Domain: virt.Domain{UUID: "vm-2", Name: "vm-2", State: virt.StateRunning}},
		},
	}
	rawSnap, _ := json.Marshal(snap)
	lv := &fakeLive{backlog: [][]byte{rawSnap}}
	srv, pl, hostID := newTestPlacementServer(t, nil, lv)

	// Seed must anti-affinity rule
	created, err := pl.AddRule(placement.Rule{
		Name:     "Web Anti-Affinity",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-1", "vm-2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := serveReq(srv, http.MethodGet, "/api/v1/placement/violations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get violations: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var viols []placement.Violation
	if err := json.Unmarshal(rec.Body.Bytes(), &viols); err != nil {
		t.Fatalf("unmarshal violations: %v", err)
	}
	if len(viols) != 1 {
		t.Fatalf("expected 1 violation, got %d (%+v)", len(viols), viols)
	}
	if viols[0].RuleID != created.ID || !viols[0].Hard || viols[0].HostID != hostID {
		t.Fatalf("unexpected violation: %+v", viols[0])
	}
}

// 6. ?local=1 writes are accepted and not re-forwarded
func TestPlacementLocalWritesAndReplication(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	admin := a.signIn(t, "onur")

	// 1. Write with ?local=1 directly to alpha: accepted on alpha, NOT forwarded to beta
	localBody := `{"name":"local-rule","kind":"vm_affinity","enabled":true,"strength":"must","domains":["x","y"]}`
	resp, body := admin.req(http.MethodPost, "/api/v1/placement/rules?local=1", localBody, "Origin", a.url)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("local write status %d: %s", resp.StatusCode, body)
	}
	var localRule placement.Rule
	if err := json.Unmarshal([]byte(body), &localRule); err != nil {
		t.Fatal(err)
	}

	// Verify alpha has the rule
	if len(a.placement.Rules()) != 1 {
		t.Fatalf("expected alpha to have 1 rule, got %d", len(a.placement.Rules()))
	}
	// Verify beta does NOT have the rule
	if len(b.placement.Rules()) != 0 {
		t.Fatalf("expected beta to have 0 rules after ?local=1 write, got %d", len(b.placement.Rules()))
	}

	// 2. Normal write without ?local=1 to alpha: accepted on alpha AND replicated to beta
	clusterBody := `{"name":"cluster-rule","kind":"vm_affinity","enabled":true,"strength":"must","domains":["w","z"]}`
	resp, body = admin.req(http.MethodPost, "/api/v1/placement/rules", clusterBody, "Origin", a.url)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("cluster write status %d: %s", resp.StatusCode, body)
	}
	var clusterRule placement.Rule
	if err := json.Unmarshal([]byte(body), &clusterRule); err != nil {
		t.Fatal(err)
	}

	// Verify beta received the replicated rule with the same ID
	betaRules := b.placement.Rules()
	if len(betaRules) != 1 || betaRules[0].ID != clusterRule.ID {
		t.Fatalf("expected beta to receive replicated rule with ID %s, got %+v", clusterRule.ID, betaRules)
	}

	// Clean up for other tests
	_ = a.placement.DeleteRule(localRule.ID)
	_ = a.placement.DeleteRule(clusterRule.ID)
	_ = b.placement.DeleteRule(clusterRule.ID)
}

// 7. Backward compatibility: DRS endpoints translate both directions
func TestPlacementDRSCompat(t *testing.T) {
	srv, pl, _ := newTestPlacementServer(t, nil, nil)
	drsEng, err := drs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv.drs = drsEng

	// POST /api/v1/cluster/drs/rules translates drs.AffinityRule -> placement.Rule
	drsRuleBody := `{"name":"compat-anti","kind":"vm_anti_affinity","mandatory":true,"domains":["x","y"]}`
	rec := serveReq(srv, http.MethodPost, "/api/v1/cluster/drs/rules", drsRuleBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create DRS rule: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var createdDRS drs.AffinityRule
	if err := json.Unmarshal(rec.Body.Bytes(), &createdDRS); err != nil {
		t.Fatal(err)
	}
	if createdDRS.ID == "" || !createdDRS.Mandatory {
		t.Fatalf("unexpected created DRS rule: %+v", createdDRS)
	}

	// Check that placement store has it stored as placement.Rule
	plRules := pl.Rules()
	if len(plRules) != 1 {
		t.Fatalf("expected placement store to have 1 rule, got %d", len(plRules))
	}
	if plRules[0].ID != createdDRS.ID || plRules[0].Kind != placement.VMAntiAffinity || plRules[0].Strength != placement.Must {
		t.Fatalf("unexpected placement rule in store: %+v", plRules[0])
	}

	// POST disabled DRS rule preserves Enabled=false in placement store
	disabledDRSBody := `{"name":"compat-disabled","kind":"vm_anti_affinity","enabled":false,"mandatory":true,"domains":["x","y"]}`
	rec = serveReq(srv, http.MethodPost, "/api/v1/cluster/drs/rules", disabledDRSBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create disabled DRS rule: status %d, body: %s", rec.Code, rec.Body.String())
	}
	var createdDisabled drs.AffinityRule
	if err := json.Unmarshal(rec.Body.Bytes(), &createdDisabled); err != nil {
		t.Fatal(err)
	}
	if createdDisabled.Enabled {
		t.Fatalf("expected created DRS rule to have Enabled=false, got true")
	}
	var foundDisabledInStore bool
	for _, r := range pl.Rules() {
		if r.ID == createdDisabled.ID {
			foundDisabledInStore = true
			if r.Enabled {
				t.Fatalf("expected placement rule in store to have Enabled=false, got true")
			}
		}
	}
	if !foundDisabledInStore {
		t.Fatalf("disabled rule not found in placement store")
	}
	_ = pl.DeleteRule(createdDisabled.ID)

	// Unsupported DRS kind returns 422
	badKindBody := `{"name":"bad-kind","kind":"invalid_affinity","mandatory":true,"domains":["x","y"]}`
	rec = serveReq(srv, http.MethodPost, "/api/v1/cluster/drs/rules", badKindBody)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unsupported DRS rule kind, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// Add vm_host rule directly to placement store and check GET /api/v1/cluster/drs translates it
	_, err = pl.AddRule(placement.Rule{
		ID:       "0123456789abcdef",
		Name:     "pinned-host",
		Kind:     placement.VMHost,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-pinned"},
		Hosts:    []string{"host-a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec = serveReq(srv, http.MethodGet, "/api/v1/cluster/drs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get DRS status: status %d", rec.Code)
	}
	var drsStatus drs.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &drsStatus); err != nil {
		t.Fatal(err)
	}
	if len(drsStatus.Rules) != 2 {
		t.Fatalf("expected 2 rules in DRS status, got %d (%+v)", len(drsStatus.Rules), drsStatus.Rules)
	}
	var foundPinned bool
	for _, r := range drsStatus.Rules {
		if r.ID == "0123456789abcdef" {
			foundPinned = true
			if r.Kind != drs.AffinityVMHost || !r.Mandatory || r.TargetHost != "host-a" {
				t.Fatalf("unexpected translated host rule in DRS status: %+v", r)
			}
		}
	}
	if !foundPinned {
		t.Fatalf("pinned host rule not found in DRS status rules")
	}

	// DELETE /api/v1/cluster/drs/rules/{id} deletes through placement store
	rec = serveReq(srv, http.MethodDelete, "/api/v1/cluster/drs/rules/"+createdDRS.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete DRS rule: status %d", rec.Code)
	}
	if len(pl.Rules()) != 1 || pl.Rules()[0].ID != "0123456789abcdef" {
		t.Fatalf("expected 1 remaining rule in placement store, got %+v", pl.Rules())
	}
}

func TestNilPlacementStoreReturns404(t *testing.T) {
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		HostName: "local",
	})
	endpoints := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/placement"},
		{http.MethodPost, "/api/v1/placement/rules"},
		{http.MethodPut, "/api/v1/placement/rules/123"},
		{http.MethodDelete, "/api/v1/placement/rules/123"},
		{http.MethodPut, "/api/v1/placement/hosts/local/labels"},
		{http.MethodGet, "/api/v1/placement/violations"},
		{http.MethodPost, "/api/v1/placement/check"},
		{http.MethodPost, "/api/v1/cluster/drs/rules"},
		{http.MethodDelete, "/api/v1/cluster/drs/rules/123"},
	}

	for _, ep := range endpoints {
		rec := serveReq(srv, ep.method, ep.path, "{}")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s with nil placement returned %d, want 404", ep.method, ep.path, rec.Code)
		}
	}
}

// 8. Host labels replication across cluster members
func TestPlacementHostLabelsReplication(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	admin := a.signIn(t, "onur")

	// Set labels on node a with ?local=1: updated on a, not forwarded to b
	localBody := `{"labels":{"tier":"gold","env":"dev"}}`
	resp, body := admin.req(http.MethodPut, "/api/v1/placement/hosts/"+a.store.SelfID()+"/labels?local=1", localBody, "Origin", a.url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local set labels status %d: %s", resp.StatusCode, body)
	}
	if a.placement.Labels()[a.store.SelfID()]["tier"] != "gold" {
		t.Fatalf("expected alpha to have gold label, got %+v", a.placement.Labels())
	}
	if b.placement.Labels()[a.store.SelfID()] != nil {
		t.Fatalf("expected beta not to have labels after ?local=1 write, got %+v", b.placement.Labels())
	}

	// Set labels on node a without ?local=1: updated on a AND replicated to b
	clusterBody := `{"labels":{"tier":"platinum","env":"prod"}}`
	resp, body = admin.req(http.MethodPut, "/api/v1/placement/hosts/"+a.store.SelfID()+"/labels", clusterBody, "Origin", a.url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cluster set labels status %d: %s", resp.StatusCode, body)
	}
	if a.placement.Labels()[a.store.SelfID()]["tier"] != "platinum" {
		t.Fatalf("expected alpha to have platinum label, got %+v", a.placement.Labels())
	}
	betaLabels := b.placement.Labels()
	if betaLabels[a.store.SelfID()] == nil || betaLabels[a.store.SelfID()]["tier"] != "platinum" || betaLabels[a.store.SelfID()]["env"] != "prod" {
		t.Fatalf("expected beta to receive replicated labels, got %+v", betaLabels)
	}
}

// 9. getPlacement multi-node aggregation
func TestPlacementGetMultiNodeAggregation(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	joinNodes(t, a, b)
	admin := a.signIn(t, "onur")

	// Set live snapshots with running VMs on both node a and node b
	snapA := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-a1", Name: "vm-a1", State: virt.StateRunning}},
			{Domain: virt.Domain{UUID: "vm-a2", Name: "vm-a2", State: virt.StateRunning}},
		},
	}
	rawA, _ := json.Marshal(snapA)
	a.lv.backlog = [][]byte{rawA}

	snapB := live.Snapshot{
		Domains: []live.DomainLive{
			{Domain: virt.Domain{UUID: "vm-b1", Name: "vm-b1", State: virt.StateRunning}},
		},
	}
	rawB, _ := json.Marshal(snapB)
	b.lv.backlog = [][]byte{rawB}

	// Add anti-affinity rule for vm-a1 and vm-a2 (both on node a -> violation)
	_, err := a.placement.AddRule(placement.Rule{
		ID:       "1111111111111111",
		Name:     "separate-a",
		Kind:     placement.VMAntiAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-a1", "vm-a2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Add affinity rule for vm-a1 and vm-b1 (spread across a and b -> violation)
	_, err = a.placement.AddRule(placement.Rule{
		ID:       "2222222222222222",
		Name:     "collocate-ab",
		Kind:     placement.VMAffinity,
		Enabled:  true,
		Strength: placement.Must,
		Domains:  []string{"vm-a1", "vm-b1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Call GET /api/v1/placement on node a
	resp, body := admin.req(http.MethodGet, "/api/v1/placement", "", "Origin", a.url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get placement status %d: %s", resp.StatusCode, body)
	}

	var getResp struct {
		Rules      []placement.Rule             `json:"rules"`
		HostLabels map[string]map[string]string `json:"hostLabels"`
		Violations []placement.Violation        `json:"violations"`
	}
	if err := json.Unmarshal([]byte(body), &getResp); err != nil {
		t.Fatalf("unmarshal get placement: %v", err)
	}

	if len(getResp.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(getResp.Rules))
	}

	// Must detect both violations across multiple nodes
	if len(getResp.Violations) != 2 {
		t.Fatalf("expected 2 violations aggregated across nodes, got %d (%+v)", len(getResp.Violations), getResp.Violations)
	}
	var foundAnti, foundAff bool
	for _, v := range getResp.Violations {
		if v.RuleID == "1111111111111111" {
			foundAnti = true
		}
		if v.RuleID == "2222222222222222" {
			foundAff = true
		}
	}
	if !foundAnti || !foundAff {
		t.Fatalf("expected both anti-affinity and affinity violations across nodes, got %+v", getResp.Violations)
	}

	// Also verify GET /api/v1/placement/violations returns the same aggregated violations
	resp, body = admin.req(http.MethodGet, "/api/v1/placement/violations", "", "Origin", a.url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get violations status %d: %s", resp.StatusCode, body)
	}
	var viols []placement.Violation
	if err := json.Unmarshal([]byte(body), &viols); err != nil {
		t.Fatalf("unmarshal violations: %v", err)
	}
	if len(viols) != 2 {
		t.Fatalf("expected 2 violations from /violations, got %d", len(viols))
	}
}
