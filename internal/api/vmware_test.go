package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/virt"
	"github.com/onurbaydas/virtomir/internal/vmware"
)

type fakeVMwareStore struct {
	mu  sync.Mutex
	cfg vmware.Connection
}

func (s *fakeVMwareStore) Config() vmware.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	if c.Password != "" {
		c.Password = vmware.Secret
	}
	return c
}

func (s *fakeVMwareStore) Connection() vmware.Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

func (s *fakeVMwareStore) SetConfig(c vmware.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.URL != "" {
		if c.Password == vmware.Secret {
			c.Password = s.cfg.Password
		}
		if c.Password == "" {
			return vmware.ErrInvalid
		}
		if !strings.HasPrefix(c.URL, "https://") {
			return vmware.ErrInvalid
		}
	} else {
		c = vmware.Connection{}
	}
	s.cfg = c
	return nil
}

type fakeInventoryLister struct {
	inv vmware.Inventory
	err error
}

func (f *fakeInventoryLister) List(ctx context.Context, c vmware.Connection) (vmware.Inventory, error) {
	if f.err != nil {
		return vmware.Inventory{}, f.err
	}
	return f.inv, nil
}

type fakeVMConverter struct {
	mu           sync.Mutex
	preflightErr error
	convertUUID  string
	convertErr   error
	shouldFailVM string
	lastOpts     vmware.ConvertOpts
}

func (f *fakeVMConverter) Preflight(ctx context.Context, c vmware.Connection, vm vmware.VM, poolFreeBytes uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.preflightErr
}

func (f *fakeVMConverter) Convert(ctx context.Context, c vmware.Connection, vm vmware.VM, opts vmware.ConvertOpts, report func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastOpts = opts
	if report != nil {
		report("converting " + vm.Name)
	}
	if f.shouldFailVM != "" && (vm.ID == f.shouldFailVM || vm.Name == f.shouldFailVM) {
		return "", errors.New("simulated conversion failure on " + vm.Name)
	}
	if f.convertErr != nil {
		return "", f.convertErr
	}
	uuid := f.convertUUID
	if uuid == "" {
		uuid = "vmware-adopted-uuid-9999"
	}
	return uuid, nil
}

type vmwareBackend struct {
	fakeBackend
	pools []virt.Pool
}

func (b *vmwareBackend) ListPools() ([]virt.Pool, error) {
	if b.pools != nil {
		return b.pools, nil
	}
	return []virt.Pool{
		{Name: "default", Path: "/var/lib/libvirt/images", AvailableBytes: 500 << 30},
	}, nil
}

func testVMwareServer(t *testing.T, actLog *activity.Log) (http.Handler, *fakeVMwareStore, *fakeInventoryLister, *fakeVMConverter, *vmware.Registry) {
	t.Helper()
	store := &fakeVMwareStore{
		cfg: vmware.Connection{
			URL:      "https://vcenter.example.com",
			Username: "administrator@vsphere.local",
			Password: "supersecret-hunter2",
		},
	}
	lister := &fakeInventoryLister{
		inv: vmware.Inventory{
			Datacenters: []vmware.Datacenter{{Name: "Datacenter-1"}},
			Hosts:       []vmware.ESXHost{{Name: "esx-01.corp", Datacenter: "Datacenter-1"}},
			VMs: []vmware.VM{
				{
					ID:         "vm-101",
					Name:       "web-server",
					Datacenter: "Datacenter-1",
					Host:       "esx-01.corp",
					PowerState: "off",
					VCPUs:      4,
					MemoryMiB:  8192,
					Disks:      []vmware.VMDisk{{Datastore: "datastore1", CapacityBytes: 40 << 30}},
				},
				{
					ID:         "vm-102",
					Name:       "db-server",
					Datacenter: "Datacenter-1",
					Host:       "esx-01.corp",
					PowerState: "off",
					VCPUs:      8,
					MemoryMiB:  16384,
					Disks:      []vmware.VMDisk{{Datastore: "datastore1", CapacityBytes: 100 << 30}},
				},
			},
		},
	}
	conv := &fakeVMConverter{}
	reg := vmware.NewRegistry()
	jm := jobs.New(context.Background())
	b := &vmwareBackend{}

	authMgr := auth.New(stubAuthn{}, func(u string) auth.Role {
		switch u {
		case "onur":
			return auth.Admin
		case "op":
			return auth.Operator
		case "view":
			return auth.Viewer
		}
		return auth.NoAccess
	})

	opts := Options{
		Backend:         b,
		Live:            &fakeLive{},
		Jobs:            jm,
		Auth:            authMgr,
		Version:         "test",
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Activity:        actLog,
		VMwareStore:     store,
		VMwareLister:    lister,
		VMwarePreflight: conv,
		VMwareConverter: conv,
		VMwareWaves:     reg,
	}

	return NewServer(opts).Handler(), store, lister, conv, reg
}

func loginCookie(t *testing.T, h http.Handler, user string) *http.Cookie {
	t.Helper()
	rec := do(h, http.MethodPost, "/api/v1/auth/login", fmt.Sprintf(`{"user":%q,"password":"pw"}`, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed for %s: %d %s", user, rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("no cookie returned for %s", user)
	}
	return cookies[0]
}

func pollJob(t *testing.T, h http.Handler, cookie *http.Cookie, jobID string) jobs.Job {
	t.Helper()
	var j jobs.Job
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec := do(h, http.MethodGet, "/api/v1/jobs/"+jobID, "", cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll job status %d: %s", rec.Code, rec.Body.String())
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		if j.State != jobs.Running {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s timed out in state %s", jobID, j.State)
	return j
}

// 1. Connection round-trip returns the masked password and persists across a masked PUT.
func TestVMwareConnectionRoundTrip(t *testing.T) {
	const rawPW = "supersecret-hunter2"
	h, store, _, _, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	// GET masked password
	rec := do(h, http.MethodGet, "/api/v1/vmware/connection", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /connection status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), rawPW) {
		t.Fatalf("raw password leaked in GET /connection: %s", rec.Body.String())
	}
	var c vmware.Connection
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Password != vmware.Secret {
		t.Fatalf("password = %q, want %q", c.Password, vmware.Secret)
	}

	// PUT with masked password retains the original password
	c.Username = "admin-updated@vsphere.local"
	putBody, _ := json.Marshal(c)
	rec = do(h, http.MethodPut, "/api/v1/vmware/connection", string(putBody), cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /connection status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), rawPW) {
		t.Fatalf("raw password leaked in PUT /connection response: %s", rec.Body.String())
	}
	if store.Connection().Password != rawPW {
		t.Fatalf("stored password lost after masked PUT: %q", store.Connection().Password)
	}
	if store.Connection().Username != "admin-updated@vsphere.local" {
		t.Fatalf("username not updated: %q", store.Connection().Username)
	}

	// Invalid PUT returns 422
	badReq := `{"url":"not-a-valid-https-url","username":"u","password":"p"}`
	rec = do(h, http.MethodPut, "/api/v1/vmware/connection", badReq, cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid PUT status = %d, want 422: %s", rec.Code, rec.Body.String())
	}

	// DELETE clears connection
	rec = do(h, http.MethodDelete, "/api/v1/vmware/connection", "", cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /connection status = %d", rec.Code)
	}
	if store.Connection().URL != "" || store.Connection().Password != "" {
		t.Fatalf("connection not cleared after DELETE: %+v", store.Connection())
	}
}

// 2. Test endpoint reports ok and failure distinctly.
func TestVMwareConnectionTest(t *testing.T) {
	h, store, lister, _, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	// Success case
	rec := do(h, http.MethodPost, "/api/v1/vmware/connection/test", "{}", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /connection/test status = %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("test reported failure, want ok=true: %s", res.Message)
	}

	// Failure case
	lister.err = errors.New("soap fault: Cannot complete login due to an incorrect user name or password")
	rec = do(h, http.MethodPost, "/api/v1/vmware/connection/test", "{}", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /connection/test failure status = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("test reported ok=true on error, want ok=false")
	}
	if !strings.Contains(res.Message, "incorrect user name or password") {
		t.Fatalf("unexpected message: %s", res.Message)
	}

	// Unconfigured store
	_ = store.SetConfig(vmware.Connection{})
	rec = do(h, http.MethodPost, "/api/v1/vmware/connection/test", "{}", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("unconfigured test status = %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.OK {
		t.Fatal("unconfigured connection reported ok=true")
	}
}

// 3. Inventory proxies the fake lister.
func TestVMwareInventory(t *testing.T) {
	h, _, _, _, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	rec := do(h, http.MethodGet, "/api/v1/vmware/inventory", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /inventory status = %d: %s", rec.Code, rec.Body.String())
	}
	var inv vmware.Inventory
	if err := json.Unmarshal(rec.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if len(inv.Datacenters) != 1 || inv.Datacenters[0].Name != "Datacenter-1" {
		t.Errorf("datacenters mismatch: %+v", inv.Datacenters)
	}
	if len(inv.Hosts) != 1 || inv.Hosts[0].Name != "esx-01.corp" {
		t.Errorf("hosts mismatch: %+v", inv.Hosts)
	}
	if len(inv.VMs) != 2 || inv.VMs[0].ID != "vm-101" || inv.VMs[1].ID != "vm-102" {
		t.Errorf("vms mismatch: %+v", inv.VMs)
	}
}

// 4. Preflight returns per-VM issues and pool free space.
func TestVMwarePreflight(t *testing.T) {
	h, _, _, conv, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	conv.preflightErr = vmware.Issues{"Shut it down first", "Not enough disk space"}
	reqBody := `{"vmIds":["vm-101","vm-unknown"],"pool":"default"}`
	rec := do(h, http.MethodPost, "/api/v1/vmware/preflight", reqBody, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /preflight status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Pool          string              `json:"pool"`
		PoolFreeBytes uint64              `json:"poolFreeBytes"`
		Issues        map[string][]string `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Pool != "default" || resp.PoolFreeBytes == 0 {
		t.Errorf("preflight pool info missing: %+v", resp)
	}
	if len(resp.Issues["vm-101"]) != 2 {
		t.Errorf("issues for vm-101 = %+v, want 2", resp.Issues["vm-101"])
	}
	if len(resp.Issues["vm-unknown"]) == 0 {
		t.Errorf("missing issue for unknown vm: %+v", resp.Issues)
	}
}

// 5. Import returns 202 with a running job that ends succeeded and produces the UUID.
func TestVMwareImport(t *testing.T) {
	actLog, _ := activity.Open("")
	h, _, _, conv, _ := testVMwareServer(t, actLog)
	cookie := loginCookie(t, h, "onur")

	conv.convertUUID = "imported-uuid-7777"
	reqBody := `{"vmId":"vm-101","pool":"default","name":"migrated-web"}`
	rec := do(h, http.MethodPost, "/api/v1/vmware/import", reqBody, cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /import status = %d: %s", rec.Code, rec.Body.String())
	}

	var j jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if j.ID == "" {
		t.Fatal("empty job ID returned from POST /import")
	}

	finished := pollJob(t, h, cookie, j.ID)
	if finished.State != jobs.Succeeded {
		t.Fatalf("job state = %s, err = %s", finished.State, finished.Message)
	}
	if finished.Result != "imported-uuid-7777" {
		t.Fatalf("job result = %q, want imported-uuid-7777", finished.Result)
	}

	// Verify activity was recorded
	entries := actLog.List(10, activity.Filter{})
	var foundStart, foundFinish bool
	for _, e := range entries {
		if strings.Contains(e.Path, "/api/v1/vmware/import") {
			if strings.Contains(e.Error, "vmware import started") {
				foundStart = true
			}
			if strings.Contains(e.Error, "vmware import finished") && strings.Contains(e.Error, "imported-uuid-7777") {
				foundFinish = true
			}
		}
	}
	if !foundStart || !foundFinish {
		t.Errorf("audit entries missing for import: start=%t finish=%t, entries=%+v", foundStart, foundFinish, entries)
	}
}

// 6. Waves endpoints list per-item states.
func TestVMwareWaves(t *testing.T) {
	actLog, _ := activity.Open("")
	h, _, _, _, _ := testVMwareServer(t, actLog)
	cookie := loginCookie(t, h, "onur")

	reqBody := `{"vmIds":["vm-101","vm-102"],"pool":"default","parallel":2}`
	rec := do(h, http.MethodPost, "/api/v1/vmware/waves", reqBody, cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /waves status = %d: %s", rec.Code, rec.Body.String())
	}

	var j jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	pollJob(t, h, cookie, j.ID)

	// GET /api/v1/vmware/waves
	rec = do(h, http.MethodGet, "/api/v1/vmware/waves", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /waves status = %d: %s", rec.Code, rec.Body.String())
	}
	var wavesResp struct {
		Waves []vmware.Wave `json:"waves"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wavesResp); err != nil {
		t.Fatal(err)
	}
	if len(wavesResp.Waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(wavesResp.Waves))
	}
	wave := wavesResp.Waves[0]
	if len(wave.Items) != 2 {
		t.Fatalf("expected 2 wave items, got %d", len(wave.Items))
	}
	for _, it := range wave.Items {
		if it.State != vmware.WaveItemDone {
			t.Errorf("item %s state = %s, want done", it.VMID, it.State)
		}
	}

	// GET /api/v1/vmware/waves/{id}
	rec = do(h, http.MethodGet, "/api/v1/vmware/waves/"+wave.ID, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /waves/{id} status = %d: %s", rec.Code, rec.Body.String())
	}
	var singleWave vmware.Wave
	if err := json.Unmarshal(rec.Body.Bytes(), &singleWave); err != nil {
		t.Fatal(err)
	}
	if singleWave.ID != wave.ID || len(singleWave.Items) != 2 {
		t.Errorf("wave details mismatch: %+v", singleWave)
	}
}

// 7. Retry spawns a job and flips failed items.
func TestVMwareWaveRetry(t *testing.T) {
	h, _, _, conv, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	// First make vm-102 fail
	conv.shouldFailVM = "vm-102"
	reqBody := `{"vmIds":["vm-101","vm-102"],"pool":"default","parallel":1}`
	rec := do(h, http.MethodPost, "/api/v1/vmware/waves", reqBody, cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /waves status = %d: %s", rec.Code, rec.Body.String())
	}
	var j jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	pollJob(t, h, cookie, j.ID)

	// Fetch wave and assert vm-101 is done, vm-102 is failed
	rec = do(h, http.MethodGet, "/api/v1/vmware/waves", "", cookie)
	var wavesResp struct {
		Waves []vmware.Wave `json:"waves"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &wavesResp)
	wave := wavesResp.Waves[0]
	var vm101Done, vm102Failed bool
	for _, it := range wave.Items {
		if it.VMID == "vm-101" && it.State == vmware.WaveItemDone {
			vm101Done = true
		}
		if it.VMID == "vm-102" && it.State == vmware.WaveItemFailed {
			vm102Failed = true
		}
	}
	if !vm101Done || !vm102Failed {
		t.Fatalf("initial wave items state wrong: %+v", wave.Items)
	}

	// Now fix the failure so retry succeeds
	conv.mu.Lock()
	conv.shouldFailVM = ""
	conv.mu.Unlock()

	// Call POST /api/v1/vmware/waves/{id}/retry
	rec = do(h, http.MethodPost, "/api/v1/vmware/waves/"+wave.ID+"/retry", `{"pool":"default"}`, cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /waves/{id}/retry status = %d: %s", rec.Code, rec.Body.String())
	}
	var retryJob jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &retryJob)
	pollJob(t, h, cookie, retryJob.ID)

	// Verify failed item flipped to done
	rec = do(h, http.MethodGet, "/api/v1/vmware/waves/"+wave.ID, "", cookie)
	var updatedWave vmware.Wave
	_ = json.Unmarshal(rec.Body.Bytes(), &updatedWave)
	for _, it := range updatedWave.Items {
		if it.VMID == "vm-102" && it.State != vmware.WaveItemDone {
			t.Errorf("failed item did not flip to done: %+v", it)
		}
	}
}

// 8. Unauthenticated/low-role requests are rejected.
func TestVMwareRBAC(t *testing.T) {
	h, _, _, _, _ := testVMwareServer(t, nil)
	opCookie := loginCookie(t, h, "op")
	viewCookie := loginCookie(t, h, "view")
	adminCookie := loginCookie(t, h, "onur")

	endpoints := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/vmware/connection", ""},
		{http.MethodPut, "/api/v1/vmware/connection", `{"url":"https://vc.local","username":"u","password":"••••••••"}`},
		{http.MethodPost, "/api/v1/vmware/connection/test", "{}"},
		{http.MethodGet, "/api/v1/vmware/inventory", ""},
		{http.MethodPost, "/api/v1/vmware/preflight", `{"vmIds":["vm-101"],"pool":"default"}`},
		{http.MethodPost, "/api/v1/vmware/import", `{"vmId":"vm-101","pool":"default"}`},
		{http.MethodGet, "/api/v1/vmware/waves", ""},
		{http.MethodPost, "/api/v1/vmware/waves", `{"vmIds":["vm-101"],"pool":"default"}`},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			// Unauthenticated -> 401
			rec := do(h, ep.method, ep.path, ep.body, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated: status = %d, want 401", rec.Code)
			}

			// Viewer role -> 403
			rec = do(h, ep.method, ep.path, ep.body, viewCookie)
			if rec.Code != http.StatusForbidden {
				t.Errorf("viewer role: status = %d, want 403", rec.Code)
			}

			// Operator role -> 403
			rec = do(h, ep.method, ep.path, ep.body, opCookie)
			if rec.Code != http.StatusForbidden {
				t.Errorf("operator role: status = %d, want 403", rec.Code)
			}

			// Admin role -> allowed (200, 202, etc. not 401/403)
			rec = do(h, ep.method, ep.path, ep.body, adminCookie)
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Errorf("admin role unexpectedly rejected: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// 9. No password ever appears in any response body.
func TestVMwareNoPasswordLeak(t *testing.T) {
	const rawPW = "supersecret-hunter2"
	h, _, lister, _, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	assertNoLeak := func(name string, rec *http.Response) {
		body, _ := io.ReadAll(rec.Body)
		if strings.Contains(string(body), rawPW) {
			t.Fatalf("raw password leaked in %s: %s", name, string(body))
		}
	}

	// GET connection
	rec := do(h, http.MethodGet, "/api/v1/vmware/connection", "", cookie)
	assertNoLeak("GET /connection", rec.Result())

	// PUT connection
	rec = do(h, http.MethodPut, "/api/v1/vmware/connection", `{"url":"https://vcenter.example.com","username":"adm","password":"`+rawPW+`"}`, cookie)
	assertNoLeak("PUT /connection", rec.Result())

	// POST connection test success
	rec = do(h, http.MethodPost, "/api/v1/vmware/connection/test", "{}", cookie)
	assertNoLeak("POST /connection/test ok", rec.Result())

	// POST connection test error containing password
	lister.err = errors.New("auth failed with password " + rawPW)
	rec = do(h, http.MethodPost, "/api/v1/vmware/connection/test", "{}", cookie)
	assertNoLeak("POST /connection/test err", rec.Result())

	// GET inventory
	lister.err = nil
	rec = do(h, http.MethodGet, "/api/v1/vmware/inventory", "", cookie)
	assertNoLeak("GET /inventory", rec.Result())

	// Waves list
	rec = do(h, http.MethodGet, "/api/v1/vmware/waves", "", cookie)
	assertNoLeak("GET /waves", rec.Result())
}

// 10. oneConverterAdapter unit tests.
func TestOneConverterAdapter(t *testing.T) {
	conn := vmware.Connection{URL: "https://vc.local", Username: "u", Password: "p"}
	inv := vmware.Inventory{
		VMs: []vmware.VM{
			{ID: "vm-1", Name: "app-server", Disks: []vmware.VMDisk{{Datastore: "ds1", CapacityBytes: 50 << 30}}},
		},
	}
	opts := vmware.ConvertOpts{
		PoolPath:   "/var/lib/images",
		NetworkMap: map[string]string{"VM Network": "virbr0"},
		VirtioISO:  "/tmp/virtio.iso",
	}

	t.Run("success", func(t *testing.T) {
		lister := &fakeInventoryLister{inv: inv}
		conv := &fakeVMConverter{}
		adapter := &oneConverterAdapter{
			lister:        lister,
			preflight:     conv,
			converter:     conv,
			opts:          opts,
			poolFreeBytes: 200 << 30,
		}

		err := adapter.ConvertOne(context.Background(), conn, "vm-1", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		conv.mu.Lock()
		defer conv.mu.Unlock()
		if conv.lastOpts.Name != "app-server" {
			t.Errorf("lastOpts.Name = %q, want app-server", conv.lastOpts.Name)
		}
		if conv.lastOpts.PoolPath != opts.PoolPath {
			t.Errorf("lastOpts.PoolPath = %q, want %q", conv.lastOpts.PoolPath, opts.PoolPath)
		}
		if conv.lastOpts.NetworkMap["VM Network"] != "virbr0" {
			t.Errorf("lastOpts.NetworkMap mismatch: %+v", conv.lastOpts.NetworkMap)
		}
		if conv.lastOpts.VirtioISO != opts.VirtioISO {
			t.Errorf("lastOpts.VirtioISO = %q, want %q", conv.lastOpts.VirtioISO, opts.VirtioISO)
		}
	})

	t.Run("lister error", func(t *testing.T) {
		lister := &fakeInventoryLister{err: errors.New("connection failed")}
		conv := &fakeVMConverter{}
		adapter := &oneConverterAdapter{
			lister:    lister,
			converter: conv,
		}
		err := adapter.ConvertOne(context.Background(), conn, "vm-1", nil)
		if err == nil || !strings.Contains(err.Error(), "connection failed") {
			t.Fatalf("expected lister error, got: %v", err)
		}
	})

	t.Run("vm not found", func(t *testing.T) {
		lister := &fakeInventoryLister{inv: inv}
		conv := &fakeVMConverter{}
		adapter := &oneConverterAdapter{
			lister:    lister,
			converter: conv,
		}
		err := adapter.ConvertOne(context.Background(), conn, "vm-unknown", nil)
		if err == nil || !strings.Contains(err.Error(), "not found in VMware inventory") {
			t.Fatalf("expected not found error, got: %v", err)
		}
	})

	t.Run("preflight failure", func(t *testing.T) {
		lister := &fakeInventoryLister{inv: inv}
		conv := &fakeVMConverter{preflightErr: errors.New("out of disk space")}
		adapter := &oneConverterAdapter{
			lister:    lister,
			preflight: conv,
			converter: conv,
		}
		err := adapter.ConvertOne(context.Background(), conn, "vm-1", nil)
		if err == nil || !strings.Contains(err.Error(), "out of disk space") {
			t.Fatalf("expected preflight error, got: %v", err)
		}
	})

	t.Run("converter failure", func(t *testing.T) {
		lister := &fakeInventoryLister{inv: inv}
		conv := &fakeVMConverter{convertErr: errors.New("virt-v2v failed")}
		adapter := &oneConverterAdapter{
			lister:    lister,
			converter: conv,
		}
		err := adapter.ConvertOne(context.Background(), conn, "vm-1", nil)
		if err == nil || !strings.Contains(err.Error(), "virt-v2v failed") {
			t.Fatalf("expected converter error, got: %v", err)
		}
	})

	t.Run("nil lister fallback", func(t *testing.T) {
		conv := &fakeVMConverter{}
		adapter := &oneConverterAdapter{
			converter: conv,
			opts:      opts,
		}
		err := adapter.ConvertOne(context.Background(), conn, "vm-dummy", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		conv.mu.Lock()
		defer conv.mu.Unlock()
		if conv.lastOpts.Name != "vm-dummy" {
			t.Errorf("lastOpts.Name = %q, want vm-dummy", conv.lastOpts.Name)
		}
	})
}

// 11. Import preflight and inventory failure scenarios.
func TestVMwareImportFailures(t *testing.T) {
	t.Run("preflight failure halts job", func(t *testing.T) {
		h, _, _, conv, _ := testVMwareServer(t, nil)
		cookie := loginCookie(t, h, "onur")
		conv.preflightErr = errors.New("machine is running")

		reqBody := `{"vmId":"vm-101","pool":"default","name":"test-vm"}`
		rec := do(h, http.MethodPost, "/api/v1/vmware/import", reqBody, cookie)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /import status = %d: %s", rec.Code, rec.Body.String())
		}
		var j jobs.Job
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		finished := pollJob(t, h, cookie, j.ID)
		if finished.State != jobs.Failed {
			t.Fatalf("job state = %s, want failed", finished.State)
		}
		if !strings.Contains(finished.Message, "preflight") && !strings.Contains(finished.Message, "machine is running") {
			t.Fatalf("job message = %q, want preflight error", finished.Message)
		}
	})

	t.Run("lister failure halts job", func(t *testing.T) {
		h, _, lister, _, _ := testVMwareServer(t, nil)
		cookie := loginCookie(t, h, "onur")
		lister.err = errors.New("vcenter down")

		reqBody := `{"vmId":"vm-101","pool":"default","name":"test-vm"}`
		rec := do(h, http.MethodPost, "/api/v1/vmware/import", reqBody, cookie)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /import status = %d: %s", rec.Code, rec.Body.String())
		}
		var j jobs.Job
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		finished := pollJob(t, h, cookie, j.ID)
		if finished.State != jobs.Failed {
			t.Fatalf("job state = %s, want failed", finished.State)
		}
		if !strings.Contains(finished.Message, "vcenter down") {
			t.Fatalf("job message = %q, want vcenter down", finished.Message)
		}
	})

	t.Run("vm not found halts job", func(t *testing.T) {
		h, _, _, _, _ := testVMwareServer(t, nil)
		cookie := loginCookie(t, h, "onur")

		reqBody := `{"vmId":"vm-nonexistent","pool":"default","name":"test-vm"}`
		rec := do(h, http.MethodPost, "/api/v1/vmware/import", reqBody, cookie)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /import status = %d: %s", rec.Code, rec.Body.String())
		}
		var j jobs.Job
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		finished := pollJob(t, h, cookie, j.ID)
		if finished.State != jobs.Failed {
			t.Fatalf("job state = %s, want failed", finished.State)
		}
		if !strings.Contains(finished.Message, "not found in VMware inventory") {
			t.Fatalf("job message = %q, want not found in inventory", finished.Message)
		}
	})
}

// 12. Empty and invalid pool rejection across endpoints.
func TestVMwareEmptyOrInvalidPoolValidation(t *testing.T) {
	h, _, _, _, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"preflight empty pool", http.MethodPost, "/api/v1/vmware/preflight", `{"vmIds":["vm-101"],"pool":""}`, http.StatusUnprocessableEntity},
		{"preflight unknown pool", http.MethodPost, "/api/v1/vmware/preflight", `{"vmIds":["vm-101"],"pool":"unknown-pool"}`, http.StatusUnprocessableEntity},
		{"import empty pool", http.MethodPost, "/api/v1/vmware/import", `{"vmId":"vm-101","pool":""}`, http.StatusUnprocessableEntity},
		{"import unknown pool", http.MethodPost, "/api/v1/vmware/import", `{"vmId":"vm-101","pool":"unknown-pool"}`, http.StatusUnprocessableEntity},
		{"wave empty pool", http.MethodPost, "/api/v1/vmware/waves", `{"vmIds":["vm-101"],"pool":""}`, http.StatusUnprocessableEntity},
		{"wave unknown pool", http.MethodPost, "/api/v1/vmware/waves", `{"vmIds":["vm-101"],"pool":"unknown-pool"}`, http.StatusUnprocessableEntity},
		{"retry empty pool", http.MethodPost, "/api/v1/vmware/waves/fake-id/retry", `{"pool":""}`, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(h, tt.method, tt.path, tt.body, cookie)
			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// 13. Retry wave tests for decode error, pool validation, and passing ConvertOpts.
func TestVMwareWaveRetryOptsAndErrors(t *testing.T) {
	h, _, _, conv, _ := testVMwareServer(t, nil)
	cookie := loginCookie(t, h, "onur")

	// Trigger initial wave with failure
	conv.shouldFailVM = "vm-101"
	rec := do(h, http.MethodPost, "/api/v1/vmware/waves", `{"vmIds":["vm-101"],"pool":"default","parallel":1}`, cookie)
	var j jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	pollJob(t, h, cookie, j.ID)

	rec = do(h, http.MethodGet, "/api/v1/vmware/waves", "", cookie)
	var wavesResp struct {
		Waves []vmware.Wave `json:"waves"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &wavesResp)
	waveID := wavesResp.Waves[0].ID

	// 1. Decode error on retry
	rec = do(h, http.MethodPost, "/api/v1/vmware/waves/"+waveID+"/retry", `{"pool": invalid-json`, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("invalid json status = %d, want 400", rec.Code)
	}

	// 2. Empty pool on retry
	rec = do(h, http.MethodPost, "/api/v1/vmware/waves/"+waveID+"/retry", `{"pool":""}`, cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty pool status = %d, want 422", rec.Code)
	}

	// 3. Unknown pool on retry
	rec = do(h, http.MethodPost, "/api/v1/vmware/waves/"+waveID+"/retry", `{"pool":"no-pool"}`, cookie)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown pool status = %d, want 422", rec.Code)
	}

	// 4. Retry with valid pool, networkMap, and virtioIso
	conv.mu.Lock()
	conv.shouldFailVM = ""
	conv.mu.Unlock()

	retryBody := `{"pool":"default","networkMap":{"VM Network":"virbr0"},"virtioIso":"/iso/virtio.iso"}`
	rec = do(h, http.MethodPost, "/api/v1/vmware/waves/"+waveID+"/retry", retryBody, cookie)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d: %s", rec.Code, rec.Body.String())
	}
	var retryJob jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &retryJob)
	pollJob(t, h, cookie, retryJob.ID)

	conv.mu.Lock()
	defer conv.mu.Unlock()
	if conv.lastOpts.NetworkMap["VM Network"] != "virbr0" {
		t.Errorf("opts.NetworkMap not propagated on retry: %+v", conv.lastOpts.NetworkMap)
	}
	if conv.lastOpts.VirtioISO != "/iso/virtio.iso" {
		t.Errorf("opts.VirtioISO not propagated on retry: %q", conv.lastOpts.VirtioISO)
	}
}
