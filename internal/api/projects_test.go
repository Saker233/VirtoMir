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

	"github.com/onurbaydas/virtomir/internal/activity"
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

// projectAPIServer: global roles onur=admin, op=operator, view=viewer; project "dev"
// (op as operator, view as viewer, room for 2 machines/4 vCPUs) and project "other"
// (nobody), so endpoint permissions and effective roles can be checked separately.
func projectAPIServer(t *testing.T, doms []virt.Domain) (http.Handler, *projectBackend, *project.Store, *activity.Log) {
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
			{Kind: "user", Name: "op", Role: "operator"}, {Kind: "user", Name: "view", Role: "operator"}},
			Limits: project.Limits{Machines: 2, VCPUs: 4, MemoryMiB: 4096, DiskGiB: 100}},
		{ID: "other", Name: "Other"},
	} {
		if _, err := pj.Create(p); err != nil {
			t.Fatal(err)
		}
	}
	alog, _ := activity.Open("")
	b := &projectBackend{&fakeBackend{domains: doms}, map[string]string{}}
	h := NewServer(Options{Backend: b, Live: &fakeLive{}, Auth: m, Projects: pj, Activity: alog,
		GroupsOf: func(string) []string { return nil },
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	return h, b, pj, alog
}

func TestProjectListAndGet(t *testing.T) {
	h, _, _, _ := projectAPIServer(t, []virt.Domain{{UUID: "u-dev", Name: "web", Project: "dev"}})
	for _, c := range []struct {
		user string
		want string
	}{{"onur", "dev,other"}, {"op", "dev"}, {"view", "dev"}} {
		rec := do(h, "GET", "/api/v1/projects", "", signIn(t, h, c.user))
		if rec.Code != http.StatusOK {
			t.Fatalf("list as %s: %d %s", c.user, rec.Code, rec.Body)
		}
		var body struct{ Projects []project.Project }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		got := make([]string, len(body.Projects))
		for i, p := range body.Projects {
			got[i] = p.ID
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("list as %s = %v, want %s", c.user, got, c.want)
		}
	}

	// GET one: members and admins only; an outsider gets the same 404 as for an unknown ID.
	for _, c := range []struct {
		user string
		path string
		want int
	}{
		{"onur", "/api/v1/projects/dev", http.StatusOK},
		{"op", "/api/v1/projects/dev", http.StatusOK},
		{"view", "/api/v1/projects/dev", http.StatusOK},
		{"view", "/api/v1/projects/other", http.StatusNotFound}, // member of dev, not of other
		{"onur", "/api/v1/projects/nope", http.StatusNotFound},
	} {
		if rec := do(h, "GET", c.path, "", signIn(t, h, c.user)); rec.Code != c.want {
			t.Errorf("GET %s as %s: %d %s, want %d", c.path, c.user, rec.Code, rec.Body, c.want)
		}
	}
}

func TestProjectCreateUpdateDelete(t *testing.T) {
	h, _, pj, alog := projectAPIServer(t, nil)
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")

	// Only admins create, change and delete projects; the guard sees to that.
	for _, c := range []struct {
		method, path, body string
		cookie             *http.Cookie
		want               int
	}{
		{"POST", "/api/v1/projects", `{"id":"lab","name":"Lab","members":[{"kind":"user","name":"op","role":"operator"}]}`, op, http.StatusForbidden},
		{"POST", "/api/v1/projects", `{"id":"lab","name":"Lab","members":[{"kind":"user","name":"op","role":"operator"}]}`, view, http.StatusForbidden},
		{"POST", "/api/v1/projects", `{"id":"lab","name":"Lab","members":[{"kind":"user","name":"op","role":"operator"}]}`, onur, http.StatusCreated},
		{"POST", "/api/v1/projects", `{"id":"lab","name":"Lab again"}`, onur, http.StatusConflict},       // same ID
		{"POST", "/api/v1/projects", `{"id":"lab2","name":"Lab"}`, onur, http.StatusConflict},            // same name
		{"POST", "/api/v1/projects", `{"id":"UP","name":"Upper"}`, onur, http.StatusUnprocessableEntity}, // bad ID
		{"POST", "/api/v1/projects", `{"id":"lab3","name":"Bad","members":[{"kind":"boss","name":"x","role":"viewer"}]}`, onur, http.StatusUnprocessableEntity},
		{"PUT", "/api/v1/projects/lab", `{"name":"Laboratory","limits":{"vcpus":8}}`, op, http.StatusForbidden},
		{"PUT", "/api/v1/projects/lab", `{"name":"Laboratory","limits":{"vcpus":8}}`, onur, http.StatusOK},
		{"PUT", "/api/v1/projects/nope", `{"name":"X"}`, onur, http.StatusNotFound},
		{"DELETE", "/api/v1/projects/lab", "", op, http.StatusForbidden},
		{"DELETE", "/api/v1/projects/lab", "", onur, http.StatusNoContent},
		{"DELETE", "/api/v1/projects/lab", "", onur, http.StatusNotFound},
	} {
		if rec := do(h, c.method, c.path, c.body, c.cookie); rec.Code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, rec.Code, rec.Body, c.want)
		}
	}
	if p, ok := pj.Get("lab"); ok {
		t.Errorf("lab still there: %+v", p)
	}
	// Create, change and delete went into the audit log like other admin actions.
	for _, path := range []string{"/api/v1/projects", "/api/v1/projects/lab"} {
		found := false
		for _, e := range alog.List(200, activity.Filter{User: "onur"}) {
			if e.Path == path && e.Method != http.MethodGet && e.Status < 400 {
				found = true
			}
		}
		if !found {
			t.Errorf("no audit entry for %s", path)
		}
	}
}

func TestProjectMoveMachine(t *testing.T) {
	// u-dev and u-dev2 fill the "dev" project (2 machines, 4 vCPUs); u-other belongs to
	// "other". Moving it into "dev" must hit the target's machine limit.
	h, b, _, _ := projectAPIServer(t, []virt.Domain{
		{UUID: "u-dev", Name: "web", Project: "dev", VCPUs: 2, MaxMemoryKiB: 2048 << 10},
		{UUID: "u-dev2", Name: "web2", Project: "dev", VCPUs: 2, MaxMemoryKiB: 2048 << 10},
		{UUID: "u-other", Name: "db", Project: "other", VCPUs: 1, MaxMemoryKiB: 512 << 10},
	})
	onur, op := signIn(t, h, "onur"), signIn(t, h, "op")

	cases := []struct {
		name         string
		uuid, body   string
		cookie       *http.Cookie
		want         int
		wantProjects map[string]string
	}{
		{"operator moves out of their project", "u-dev", `{"project":""}`, op, http.StatusNoContent, map[string]string{"u-dev": ""}},
		{"operator moves back in", "u-dev", `{"project":"dev"}`, op, http.StatusNoContent, map[string]string{"u-dev": "dev"}},
		{"operator not in the source", "u-other", `{"project":"dev"}`, op, http.StatusForbidden, nil},
		{"operator not in the target", "u-dev", `{"project":"other"}`, op, http.StatusForbidden, nil},
		{"the target's limits", "u-other", `{"project":"dev"}`, onur, http.StatusConflict, nil},
		{"unknown project", "u-dev", `{"project":"nope"}`, onur, http.StatusNotFound, nil},
		{"unknown machine", "u-nope", `{"project":"dev"}`, onur, http.StatusNotFound, nil},
		{"admin moves out", "u-other", `{}`, onur, http.StatusNoContent, map[string]string{"u-other": ""}},
	}
	for _, c := range cases {
		if rec := do(h, "PUT", "/api/v1/domains/"+c.uuid+"/project", c.body, c.cookie); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
		for uuid, want := range c.wantProjects {
			if got := b.projects[uuid]; got != want {
				t.Errorf("%s: project of %s = %q, want %q", c.name, uuid, got, want)
			}
		}
	}
}

// TestProjectEffectiveRole: a global viewer who is an operator in project "dev" may run
// that project's machines, but not machines of a project they do not belong to.
func TestProjectEffectiveRole(t *testing.T) {
	h, _, _, _ := projectAPIServer(t, []virt.Domain{
		{UUID: "u-dev", Name: "web", Project: "dev", VCPUs: 2, MaxMemoryKiB: 2048 << 10},
		{UUID: "u-other", Name: "db", Project: "other", VCPUs: 1, MaxMemoryKiB: 512 << 10},
	})
	onur, op, view := signIn(t, h, "onur"), signIn(t, h, "op"), signIn(t, h, "view")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		cookie *http.Cookie
		want   int
	}{
		{"project viewer reads", "GET", "/api/v1/domains/u-dev", "", view, http.StatusOK},
		{"project operator starts own", "POST", "/api/v1/domains/u-dev/actions/start", "", view, http.StatusAccepted},
		{"but not of another project", "POST", "/api/v1/domains/u-other/actions/start", "", view, http.StatusForbidden},
		{"project operator may not delete", "DELETE", "/api/v1/domains/u-dev", "", view, http.StatusForbidden},
		{"project admin deletes", "DELETE", "/api/v1/domains/u-other", "", onur, 0},
		{"global operator still may", "POST", "/api/v1/domains/u-other/actions/start", "", op, http.StatusAccepted},
	}
	for _, c := range cases {
		rec := do(h, c.method, c.path, c.body, c.cookie)
		if c.want == 0 { // 0: any success
			if rec.Code >= 400 {
				t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
			}
		} else if rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}
}
