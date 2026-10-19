package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/catalog"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// catalogServer: the catalog (ADR-0065) wired with a project store. Project "dev"
// holds op as operator and view as project admin; project "other" has nobody.
func catalogServer(t *testing.T, doms []virt.Domain) (http.Handler, *catalog.Store, *project.Store, *activity.Log) {
	t.Helper()
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Viewer}[u]
	})
	pj, err := project.Open(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []project.Project{
		{ID: "dev", Name: "Dev", Members: []project.Member{
			{Kind: "user", Name: "op", Role: "operator"}, {Kind: "user", Name: "view", Role: "admin"}}},
		{ID: "other", Name: "Other"},
	} {
		if _, err := pj.Create(p); err != nil {
			t.Fatal(err)
		}
	}
	store, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	alog, _ := activity.Open("")
	b := &fakeBackend{domains: doms}
	lm := catalog.NewLeaseManager(store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), catalog.WithProjectStore(pj))
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Projects: pj, Catalog: store, LeaseMgr: lm,
		Activity: alog, Jobs: jobs.New(context.Background()),
		GroupsOf: func(string) []string { return nil },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	return h, store, pj, alog
}

func testBlueprint(id string, approval bool) string {
	return `{"id":"` + id + `","name":"Ubuntu Worker","sourceType":"template","sourceRef":"ubuntu-24",
		"presets":[{"id":"small","name":"Small","vcpus":2,"memoryMiB":2048,"diskGiB":20}],"requireApproval":` + boolJSON(approval) + `}`
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestCatalogBlueprintCRUD(t *testing.T) {
	h, store, _, alog := catalogServer(t, nil)
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")

	if rec := do(h, "GET", "/api/v1/catalog/blueprints", "", view); rec.Code != http.StatusOK {
		t.Fatalf("viewer lists blueprints: %d %s", rec.Code, rec.Body)
	}

	// Only admins create, change and delete blueprints; the guard sees to that.
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", false), op); rec.Code != http.StatusForbidden {
		t.Errorf("operator create: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", false), view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer create: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", false), onur); rec.Code != http.StatusCreated {
		t.Errorf("admin create: %d %s", rec.Code, rec.Body)
	}
	if got, _ := store.GetBlueprint("web"); got.ID != "web" || got.SourceRef != "ubuntu-24" {
		t.Errorf("blueprint not saved: %+v", got)
	}

	// Validation: the same ID again conflicts, a second blueprint is created fine.
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", false), onur); rec.Code != http.StatusConflict {
		t.Errorf("duplicate create: %d %s", rec.Code, rec.Body)
	}
	rec := do(h, "POST", "/api/v1/catalog/blueprints", `{"id":"database","name":"Database","sourceType":"image",
		"sourceRef":"debian-13","presets":[{"id":"m","name":"M","vcpus":4,"memoryMiB":4096,"diskGiB":50}]}`, onur)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second create: %d %s", rec.Code, rec.Body)
	}

	// Update and delete are admin work, too.
	if rec := do(h, "PUT", "/api/v1/catalog/blueprints/web", `{"name":"Ubuntu Worker v2","presets":[{"id":"small","name":"Small","vcpus":2,"memoryMiB":2048,"diskGiB":20}]}`, view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer update: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/catalog/blueprints/web", `{"name":"Ubuntu Worker v2","presets":[{"id":"small","name":"Small","vcpus":2,"memoryMiB":2048,"diskGiB":20}]}`, onur); rec.Code != http.StatusOK {
		t.Errorf("admin update: %d %s", rec.Code, rec.Body)
	}
	if got, _ := store.GetBlueprint("web"); got.Name != "Ubuntu Worker v2" {
		t.Errorf("update not saved: %+v", got)
	}
	if rec := do(h, "DELETE", "/api/v1/catalog/blueprints/database", "", view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "DELETE", "/api/v1/catalog/blueprints/database", "", onur); rec.Code != http.StatusNoContent {
		t.Errorf("admin delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/v1/catalog/blueprints/database", "", onur); rec.Code != http.StatusNotFound {
		t.Errorf("deleted blueprint: %d %s", rec.Code, rec.Body)
	}

	// Create, update and delete went into the audit log like other admin actions.
	for _, want := range []string{"CATALOG-BLUEPRINT-CREATE", "CATALOG-BLUEPRINT-UPDATE", "CATALOG-BLUEPRINT-DELETE"} {
		found := false
		for _, e := range alog.List(200, activity.Filter{User: "onur"}) {
			if e.Method == want && e.Status < 400 {
				found = true
			}
		}
		if !found {
			t.Errorf("no audit entry for %s", want)
		}
	}
}

func TestCatalogRequestSubmission(t *testing.T) {
	h, store, _, _ := catalogServer(t, nil)
	onur, op := signIn(t, h, "onur"), signIn(t, h, "op")
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", false), onur); rec.Code != http.StatusCreated {
		t.Fatalf("seed blueprint: %d %s", rec.Code, rec.Body)
	}

	// Only project members order machines: even the global admin, who does not belong
	// to "dev", is refused.
	if rec := do(h, "POST", "/api/v1/catalog/requests", `{"blueprintId":"web","projectId":"dev","vmName":"tenant-web"}`, onur); rec.Code != http.StatusForbidden {
		t.Errorf("admin outside the project: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/catalog/requests", `{"blueprintId":"web","projectId":"other","vmName":"tenant-web"}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("operator outside the project: %d %s", rec.Code, rec.Body)
	}

	// A project member gets the machine at once: the blueprint needs no approval, and
	// the preset's sizes land on the request.
	rec := do(h, "POST", "/api/v1/catalog/requests", `{"blueprintId":"web","projectId":"dev","vmName":"tenant-web","leaseDays":7}`, op)
	if rec.Code != http.StatusCreated {
		t.Fatalf("member submit: %d %s", rec.Code, rec.Body)
	}
	var saved catalog.Request
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != catalog.StatusApproved || saved.VCPUs != 2 || saved.MemoryMiB != 2048 || saved.DiskGiB != 20 {
		t.Errorf("unexpected request: %+v", saved)
	}

	// Visibility: admins see every request; members only the ones of their projects.
	// Seed one in "other", where nobody in dev belongs.
	if _, err := store.CreateRequest(catalog.Request{ID: "req-other", BlueprintID: "web", ProjectID: "other",
		Requester: "someone", VMName: "there", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	list := func(t *testing.T, path string, cookie *http.Cookie) []string {
		t.Helper()
		rec := do(h, "GET", path, "", cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", path, rec.Code, rec.Body)
		}
		var body struct {
			Requests []catalog.Request `json:"requests"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range body.Requests {
			ids = append(ids, r.ID)
		}
		return ids
	}
	// The global admin sees both; the members only their project's one.
	if got := list(t, "/api/v1/catalog/requests", onur); len(got) != 2 || !slices.Contains(got, "req-other") {
		t.Errorf("admin list = %v, want every request", got)
	}
	if got := list(t, "/api/v1/catalog/requests", op); len(got) != 1 || got[0] != saved.ID {
		t.Errorf("op list = %v, want [%s]", got, saved.ID)
	}
	// ?project narrows the list for an admin…
	if got := list(t, "/api/v1/catalog/requests?project=other", onur); len(got) != 1 || got[0] != "req-other" {
		t.Errorf("admin list other = %v", got)
	}
	// …answers nothing to a member of a project they do not belong to…
	if got := list(t, "/api/v1/catalog/requests?project=other", op); len(got) != 0 {
		t.Errorf("op list other = %v, want none", got)
	}
	// …and answers their project's requests to a member.
	if got := list(t, "/api/v1/catalog/requests?project=dev", op); len(got) != 1 || got[0] != saved.ID {
		t.Errorf("op list dev = %v", got)
	}
}

func TestCatalogRequestApproval(t *testing.T) {
	h, store, _, alog := catalogServer(t, nil)
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", true), onur); rec.Code != http.StatusCreated {
		t.Fatalf("seed blueprint: %d %s", rec.Code, rec.Body)
	}

	// With requireApproval the request waits.
	rec := do(h, "POST", "/api/v1/catalog/requests", `{"blueprintId":"web","projectId":"dev","vmName":"tenant-web"}`, view)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	var req catalog.Request
	if err := json.Unmarshal(rec.Body.Bytes(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Status != catalog.StatusPendingApproval {
		t.Fatalf("status = %q, want pending_approval", req.Status)
	}

	// A project operator may not approve; a project admin (view) may.
	if rec := do(h, "POST", "/api/v1/catalog/requests/"+req.ID+"/approve", `{"comment":"ok"}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("operator approve: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/v1/catalog/requests/"+req.ID+"/approve", `{"comment":"ok"}`, view)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("project admin approve: %d %s", rec.Code, rec.Body)
	}
	var j jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || j.Kind != "catalog-provision" {
		t.Fatalf("approval job: %+v (%v)", j, err)
	}
	got, _ := store.GetRequest(req.ID)
	if got.Status != catalog.StatusApproved || got.ReviewedBy != "view" || got.ReviewComment != "ok" {
		t.Errorf("approved request: %+v", got)
	}
	// Approved twice: 422, the request is no longer pending.
	if rec := do(h, "POST", "/api/v1/catalog/requests/"+req.ID+"/approve", "", onur); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("second approve: %d %s", rec.Code, rec.Body)
	}

	// Rejecting needs the same rights; a global admin may, and it goes into the audit
	// log with the approval.
	rec = do(h, "POST", "/api/v1/catalog/requests", `{"blueprintId":"web","projectId":"dev","vmName":"second"}`, view)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second submit: %d %s", rec.Code, rec.Body)
	}
	var req2 catalog.Request
	_ = json.Unmarshal(rec.Body.Bytes(), &req2)
	if rec := do(h, "POST", "/api/v1/catalog/requests/"+req2.ID+"/reject", `{"comment":"not now"}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("operator reject: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/catalog/requests/"+req2.ID+"/reject", `{"comment":"not now"}`, onur); rec.Code != http.StatusOK {
		t.Errorf("reject: %d %s", rec.Code, rec.Body)
	}
	if got, _ := store.GetRequest(req2.ID); got.Status != catalog.StatusRejected {
		t.Errorf("rejected request: %+v", got)
	}
	for _, want := range []string{"CATALOG-REQUEST-APPROVE", "CATALOG-REQUEST-REJECT"} {
		found := false
		for _, e := range alog.List(200, activity.Filter{}) {
			if e.Method == want && e.Status < 400 {
				found = true
			}
		}
		if !found {
			t.Errorf("no audit entry for %s", want)
		}
	}
}

func seedLeaseRequest(t *testing.T, h http.Handler, store *catalog.Store, id, projectID, requester string) {
	t.Helper()
	req := catalog.Request{ID: id, BlueprintID: "web", ProjectID: projectID, Requester: requester, VMName: id,
		Status: catalog.StatusActive, LeaseDays: 7, CreatedAt: time.Now().UTC().Add(-2 * 24 * time.Hour)}
	if _, err := store.CreateRequest(req); err != nil {
		t.Fatal(err)
	}
	lease := catalog.Lease{RequestID: id, MachineUUID: "m-" + id, ProjectID: projectID,
		Expiry: time.Now().UTC().Add(2 * 24 * time.Hour), State: catalog.LeaseStateActive}
	if err := store.SaveLease(lease); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogExtendLease(t *testing.T) {
	h, store, _, _ := catalogServer(t, nil)
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", false), onur); rec.Code != http.StatusCreated {
		t.Fatalf("seed blueprint: %d %s", rec.Code, rec.Body)
	}
	seedLeaseRequest(t, h, store, "req1", "dev", "view")
	seedLeaseRequest(t, h, store, "req2", "other", "someone")

	// view is not a global admin but is the requester: allowed.
	before, _ := store.GetLease("m-req1")
	want := before.Expiry.Add(5 * 24 * time.Hour)
	rec := do(h, "POST", "/api/v1/catalog/requests/req1/extend", `{"days":5}`, view)
	if rec.Code != http.StatusOK {
		t.Fatalf("requester extend: %d %s", rec.Code, rec.Body)
	}
	var lease catalog.Lease
	if err := json.Unmarshal(rec.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	if !lease.Expiry.Equal(want) {
		t.Errorf("expiry = %v, want %v", lease.Expiry.UTC(), want.UTC())
	}
	if lease.State != catalog.LeaseStateActive {
		t.Errorf("state = %q, want active", lease.State)
	}
	if got, _ := store.GetLease("m-req1"); !got.Expiry.Equal(want) {
		t.Errorf("expiry not saved: %v", got.Expiry.UTC())
	}

	// A project operator may extend, too…
	if rec := do(h, "POST", "/api/v1/catalog/requests/req1/extend", `{"days":1}`, op); rec.Code != http.StatusOK {
		t.Errorf("project operator extend: %d %s", rec.Code, rec.Body)
	}
	// …as may a global admin.
	if rec := do(h, "POST", "/api/v1/catalog/requests/req1/extend", `{"days":1}`, onur); rec.Code != http.StatusOK {
		t.Errorf("admin extend: %d %s", rec.Code, rec.Body)
	}
	// …but only in projects they belong to: op never joined "other".
	if rec := do(h, "POST", "/api/v1/catalog/requests/req2/extend", `{"days":1}`, op); rec.Code != http.StatusForbidden {
		t.Errorf("operator outside the project: %d %s", rec.Code, rec.Body)
	}
	// Zero and negative days are refused.
	if rec := do(h, "POST", "/api/v1/catalog/requests/req1/extend", `{"days":0}`, view); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("zero days: %d %s", rec.Code, rec.Body)
	}
	// Unknown request.
	if rec := do(h, "GET", "/api/v1/catalog/requests/nope", "", onur); rec.Code != http.StatusNotFound {
		t.Errorf("unknown request: %d %s", rec.Code, rec.Body)
	}
}

func TestCatalogRequestCancel(t *testing.T) {
	h, store, _, _ := catalogServer(t, nil)
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")
	if rec := do(h, "POST", "/api/v1/catalog/blueprints", testBlueprint("web", true), onur); rec.Code != http.StatusCreated {
		t.Fatalf("seed blueprint: %d %s", rec.Code, rec.Body)
	}

	// The requester cancels their own pending order.
	rec := do(h, "POST", "/api/v1/catalog/requests", `{"blueprintId":"web","projectId":"dev","vmName":"pending"}`, view)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	var req catalog.Request
	_ = json.Unmarshal(rec.Body.Bytes(), &req)
	if rec := do(h, "DELETE", "/api/v1/catalog/requests/"+req.ID, "", op); rec.Code != http.StatusForbidden {
		t.Errorf("non-requester cancel: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "DELETE", "/api/v1/catalog/requests/"+req.ID, "", view); rec.Code != http.StatusNoContent {
		t.Errorf("requester cancel: %d %s", rec.Code, rec.Body)
	}
	if _, ok := store.GetRequest(req.ID); ok {
		t.Error("cancelled request still there")
	}

	// A non-pending record belongs to the admins.
	if _, err := store.CreateRequest(catalog.Request{ID: "old-one", BlueprintID: "web", ProjectID: "dev",
		Requester: "view", VMName: "gone", Status: catalog.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	if rec := do(h, "DELETE", "/api/v1/catalog/requests/old-one", "", view); rec.Code != http.StatusForbidden {
		t.Errorf("requester deletes a non-pending record: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "DELETE", "/api/v1/catalog/requests/old-one", "", onur); rec.Code != http.StatusNoContent {
		t.Errorf("admin delete: %d %s", rec.Code, rec.Body)
	}
}

func TestCatalogPricing(t *testing.T) {
	h, store, _, _ := catalogServer(t, nil)
	onur, view := signIn(t, h, "onur"), signIn(t, h, "view")

	if rec := do(h, "GET", "/api/v1/catalog/pricing", "", view); rec.Code != http.StatusOK {
		t.Fatalf("viewer reads pricing: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/catalog/pricing", `{"vcpuHour":0.10,"ramGiBHour":0.02,"diskGiBMonth":0.05,"currency":"EUR"}`, view); rec.Code != http.StatusForbidden {
		t.Errorf("viewer saves pricing: %d %s", rec.Code, rec.Body)
	}
	rec := do(h, "PUT", "/api/v1/catalog/pricing", `{"vcpuHour":0.10,"ramGiBHour":0.02,"diskGiBMonth":0.05,"currency":"EUR"}`, onur)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin saves pricing: %d %s", rec.Code, rec.Body)
	}
	var answer struct {
		Pricing catalog.PricingConfig `json:"pricing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || answer.Pricing.VCPUHour != 0.10 || answer.Pricing.Currency != "EUR" {
		t.Fatalf("saved pricing = %+v (%v)", answer.Pricing, err)
	}
	if got := store.GetPricing(); got.RAMGiBHour != 0.02 {
		t.Errorf("pricing not saved: %+v", got)
	}
	// Negative rates are refused.
	if rec := do(h, "PUT", "/api/v1/catalog/pricing", `{"vcpuHour":-1,"currency":"USD"}`, onur); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("negative rate: %d %s", rec.Code, rec.Body)
	}
}

func TestCatalogShowback(t *testing.T) {
	doms := []virt.Domain{{
		UUID: "u-dev", Name: "web", Project: "dev", State: virt.StateRunning, VCPUs: 2, MaxMemoryKiB: 2048 << 10,
	}}
	h, _, _, _ := catalogServer(t, doms)
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")
	base := "/api/v1/catalog/showback?year=2026&month=6"

	// JSON is the default.
	rec := do(h, "GET", base, "", onur)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") == "" ||
		!strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("json: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var rep catalog.ShowbackReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Period != "2026-06" || rep.TotalCost <= 0 || rep.TotalMachines != 1 {
		t.Errorf("report = period %q cost %v machines %d", rep.Period, rep.TotalCost, rep.TotalMachines)
	}

	// CSV and HTML go out with their own content types.
	for _, c := range []struct {
		format, ctype string
	}{{
		"csv", "text/csv",
	}, {
		"html", "text/html",
	}} {
		rec = do(h, "GET", base+"&format="+c.format, "", onur)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status: %d %s", c.format, rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, c.ctype) {
			t.Errorf("%s content type = %q", c.format, ct)
		}
	}

	// A member with no project asked gets only their own; ?project enforces
	// membership.
	rec = do(h, "GET", base+"&project=other", "", op)
	if rec.Code != http.StatusForbidden {
		t.Errorf("member asks a foreign project: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", base+"&project=dev", "", view)
	if rec.Code != http.StatusOK {
		t.Errorf("member asks their project: %d %s", rec.Code, rec.Body)
	}
	var mine catalog.ShowbackReport
	if err := json.Unmarshal(rec.Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	if len(mine.Projects) != 1 || mine.Projects[0].ProjectID != "dev" || mine.Projects[0].ProjectName != "Dev" {
		t.Errorf("member report = %+v", mine.Projects)
	}
	// An unknown format is refused.
	if rec := do(h, "GET", base+"&format=xlsx", "", onur); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown format: %d %s", rec.Code, rec.Body)
	}
}

// TestCatalogClusterReplication: ?local=1 saves locally without forwarding to peers;
// a normal write reaches every member.
func TestCatalogClusterReplication(t *testing.T) {
	a := newTestNode(t, "alpha", nil)
	b := newTestNode(t, "beta", nil)
	for _, n := range []*testNode{a, b} {
		cs, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.json"))
		if err != nil {
			t.Fatal(err)
		}
		n.api.catalog = cs
	}
	joinNodes(t, a, b)

	admin := a.signIn(t, "onur")
	// With ?local=1 the copy stops at alpha.
	resp, body := admin.req("POST", "/api/v1/catalog/blueprints?local=1", testBlueprint("undertow", false), "Origin", a.url)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("local create: %d %s", resp.StatusCode, body)
	}
	if got, _ := a.api.catalog.GetBlueprint("undertow"); got.ID != "undertow" {
		t.Errorf("alpha blueprint: %+v", got)
	}
	if got, _ := b.api.catalog.GetBlueprint("undertow"); got.ID == "undertow" {
		t.Error("beta took the ?local=1 copy")
	}

	// Without it, both hosts have the blueprint.
	resp, body = admin.req("POST", "/api/v1/catalog/blueprints", testBlueprint("wanderer", false), "Origin", a.url)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("replicated create: %d %s", resp.StatusCode, body)
	}
	if got, _ := b.api.catalog.GetBlueprint("wanderer"); got.ID != "wanderer" {
		t.Errorf("beta blueprint after replicated create: %+v", got)
	}
}
