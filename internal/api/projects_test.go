package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/jobs"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/quota"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// projectBackend remembers which project each machine was put into (SetProject, ADR-0060).
type projectBackend struct {
	*fakeBackend
	projects map[string]string
}

func (p *projectBackend) SetProject(uuid, projectID string) error {
	p.projects[uuid] = projectID
	return nil
}

// projectServer: the "dev" project with op as operator, generous user quotas so the
// global self-service rules pass, and one machine already in the project.
func projectServer(t *testing.T, doms []virt.Domain) (http.Handler, *projectBackend) {
	t.Helper()
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "op": auth.Operator, "view": auth.Operator}[u]
	})
	q, err := quota.Open(filepath.Join(t.TempDir(), "quotas.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Set([]quota.Rule{
		{Kind: "user", Name: "op", Machines: 10, VCPUs: 32, MemoryMiB: 128 << 10},
		{Kind: "user", Name: "view", Machines: 10, VCPUs: 32, MemoryMiB: 128 << 10},
	}); err != nil {
		t.Fatal(err)
	}
	pj, err := project.Open(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pj.Create(project.Project{
		ID:           "dev",
		Name:         "Dev",
		Members:      []project.Member{{Kind: "user", Name: "op", Role: "operator"}},
		Limits:       project.Limits{Machines: 2, VCPUs: 4, MemoryMiB: 4096, DiskGiB: 100},
		StoragePools: []string{"pool1"},
		Networks:     []string{"net1"},
	}); err != nil {
		t.Fatal(err)
	}
	b := &projectBackend{&fakeBackend{domains: doms}, map[string]string{}}
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Quotas: q, Projects: pj,
		GroupsOf: func(string) []string { return nil },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	return h, b
}

func TestProjectCreate(t *testing.T) {
	// One machine already in the project: 1 of 2 machines, 2 of 4 vCPUs, 2048 of 4096 MiB.
	h, b := projectServer(t, []virt.Domain{{UUID: "u-dev", Name: "web", Project: "dev", VCPUs: 2, MaxMemoryKiB: 2048 << 10}})
	op, out, boss := signIn(t, h, "op"), signIn(t, h, "view"), signIn(t, h, "onur")

	// A project operator creates within the limits: allowed, the machine joins the project.
	rec := do(h, "POST", "/api/v1/domains", `{"name":"web2","vcpus":2,"memoryMiB":2048,"pool":"pool1","diskGiB":10,"network":{"network":"net1"},"project":"dev"}`, op)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create in project: %d %s", rec.Code, rec.Body)
	}
	if b.projects["u-new"] != "dev" {
		t.Errorf("project not recorded: %v", b.projects)
	}

	// Membership: an operator outside the project may not create in it.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"x","vcpus":1,"memoryMiB":512,"project":"dev"}`, out); rec.Code != http.StatusForbidden ||
		!strings.Contains(rec.Body.String(), "not an operator in project dev") {
		t.Errorf("non-member: %d %s", rec.Code, rec.Body)
	}
	// A global admin may.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"adm","vcpus":1,"memoryMiB":512,"project":"dev"}`, boss); rec.Code != http.StatusCreated {
		t.Errorf("admin create: %d %s", rec.Code, rec.Body)
	}

	// Allowed lists: the pool and the network must be on them.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"p","vcpus":1,"memoryMiB":512,"pool":"pool2","diskGiB":1,"project":"dev"}`, op); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "storage pool pool2 is not allowed in project dev") {
		t.Errorf("pool not allowed: %d %s", rec.Code, rec.Body)
	}
	// A new disk without a pool lands in the "default" pool: it must be allowed too.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"d","vcpus":1,"memoryMiB":512,"diskGiB":1,"project":"dev"}`, op); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "storage pool default is not allowed in project dev") {
		t.Errorf("default pool not allowed: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"n","vcpus":1,"memoryMiB":512,"network":{"network":"net2"},"project":"dev"}`, op); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "network net2 is not allowed in project dev") {
		t.Errorf("network not allowed: %d %s", rec.Code, rec.Body)
	}
	// Limits: the existing machine uses 2 of 4 vCPUs, so 3 more do not fit.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"big","vcpus":3,"memoryMiB":512,"project":"dev"}`, op); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "project") {
		t.Errorf("over the project's limits: %d %s", rec.Code, rec.Body)
	}
	// Unknown project.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"g","vcpus":1,"memoryMiB":512,"project":"nope"}`, op); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d %s", rec.Code, rec.Body)
	}
	// Without a project the machine is global: no project checks, nothing recorded.
	if rec := do(h, "POST", "/api/v1/domains", `{"name":"glob","vcpus":1,"memoryMiB":512,"pool":"anywhere","network":{"network":"wherever"}}`, op); rec.Code != http.StatusCreated {
		t.Errorf("global machine: %d %s", rec.Code, rec.Body)
	}
}

func TestProjectCloneInherits(t *testing.T) {
	src := virt.Domain{UUID: "src-1", Name: "src", Project: "dev", VCPUs: 2, MaxMemoryKiB: 2048 << 10}
	h, b := projectServer(t, []virt.Domain{src})
	boss := signIn(t, h, "onur")

	rec := do(h, "POST", "/api/v1/domains/src-1/clone", `{"name":"copy"}`, boss)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("clone: %d %s", rec.Code, rec.Body)
	}
	var j jobs.Job
	_ = json.Unmarshal(rec.Body.Bytes(), &j)
	for i := 0; i < 200; i++ {
		rec = do(h, "GET", "/api/v1/jobs/"+j.ID, "", boss)
		_ = json.Unmarshal(rec.Body.Bytes(), &j)
		if j.State != jobs.Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if j.State != jobs.Succeeded {
		t.Fatalf("job = %+v", j)
	}
	// The copy stays in its source's project.
	if b.projects["new-uuid"] != "dev" {
		t.Errorf("clone did not inherit the project: %v", b.projects)
	}
}

// TestProjectResize: growing a project machine must stay within the project's limits.
func TestProjectResize(t *testing.T) {
	mine := virt.Domain{UUID: "u1", Name: "web", Owner: "op", Project: "dev", VCPUs: 2, MaxMemoryKiB: 2048 << 10}
	h, b := projectServer(t, []virt.Domain{mine})
	op := signIn(t, h, "op")

	if rec := do(h, "PUT", "/api/v1/domains/u1/settings", `{"maxVcpus":3,"vcpus":3,"memoryMiB":2048}`, op); rec.Code != http.StatusOK {
		t.Fatalf("grow within the project: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "PUT", "/api/v1/domains/u1/settings", `{"maxVcpus":8,"vcpus":8,"memoryMiB":2048}`, op); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "project's limit of 4 vCPUs") {
		t.Errorf("grow past the project: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(strings.Join(b.calls, ","), "settings u1") {
		t.Errorf("calls: %v", b.calls)
	}
}
