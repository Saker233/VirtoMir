package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

type mockDeployer struct {
	domains      []virt.Domain
	createdSpecs []virt.CreateSpec
	deploySpecs  []virt.DeploySpec
	owners       map[string]string
	projects     map[string]string
	createErr    error
	deployErr    error
	listErr      error
	setOwnerErr  error
	setProjErr   error
	nextUUID     string
	diskBytesMap map[string]uint64
}

func (m *mockDeployer) CreateDomain(spec virt.CreateSpec) (virt.CreateResult, error) {
	if m.createErr != nil {
		return virt.CreateResult{}, m.createErr
	}
	m.createdSpecs = append(m.createdSpecs, spec)
	uuid := m.nextUUID
	if uuid == "" {
		uuid = "mock-uuid-" + spec.Name
	}
	return virt.CreateResult{UUID: uuid}, nil
}

func (m *mockDeployer) Deploy(ctx context.Context, template string, spec virt.DeploySpec, report func(string)) (string, error) {
	if m.deployErr != nil {
		return "", m.deployErr
	}
	m.deploySpecs = append(m.deploySpecs, spec)
	uuid := m.nextUUID
	if uuid == "" {
		uuid = "mock-uuid-" + spec.Name
	}
	return uuid, nil
}

func (m *mockDeployer) SetOwner(uuid, owner string) error {
	if m.setOwnerErr != nil {
		return m.setOwnerErr
	}
	if m.owners == nil {
		m.owners = make(map[string]string)
	}
	m.owners[uuid] = owner
	return nil
}

func (m *mockDeployer) SetProject(uuid, project string) error {
	if m.setProjErr != nil {
		return m.setProjErr
	}
	if m.projects == nil {
		m.projects = make(map[string]string)
	}
	m.projects[uuid] = project
	return nil
}

func (m *mockDeployer) ListDomains() ([]virt.Domain, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]virt.Domain, len(m.domains))
	copy(out, m.domains)
	return out, nil
}

func (m *mockDeployer) DiskBytes(uuids []string) (map[string]uint64, error) {
	if m.diskBytesMap != nil {
		return m.diskBytesMap, nil
	}
	return nil, nil
}

const testSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIvUnitTestKey admin@virtomir"

func setupTestStores(t *testing.T) (*Store, *project.Store) {
	t.Helper()
	dir := t.TempDir()

	cStore, err := Open(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatalf("Open catalog store: %v", err)
	}

	pStore, err := project.Open(filepath.Join(dir, "projects.json"))
	if err != nil {
		t.Fatalf("Open project store: %v", err)
	}

	return cStore, pStore
}

func TestWorkflow_SubmitRequest_ApprovalRequired(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-a",
		Name: "Project A",
		Members: []project.Member{
			{Kind: "user", Name: "alice", Role: "viewer"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:              "bp-needs-approval",
		Name:            "Ubuntu with approval",
		SourceType:      SourceTypeImage,
		SourceRef:       "ubuntu-24.04.qcow2",
		RequireApproval: true,
		Presets: []SizePreset{
			{ID: "small", Name: "Small", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20},
		},
		DefaultPresetID:  "small",
		DefaultLeaseDays: 14,
		MaxLeaseDays:     30,
		DefaultSSHKeys:   []string{testSSHKey},
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	req := Request{
		BlueprintID: "bp-needs-approval",
		ProjectID:   "proj-a",
		VMName:      "alice-vm-1",
		PresetID:    "small",
	}

	submitted, err := SubmitRequest(cStore, pStore, "alice", nil, req)
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	if submitted.Status != StatusPendingApproval {
		t.Fatalf("expected status %q, got %q", StatusPendingApproval, submitted.Status)
	}
	if submitted.Requester != "alice" {
		t.Fatalf("expected requester alice, got %q", submitted.Requester)
	}
	if submitted.VCPUs != 2 || submitted.MemoryMiB != 2048 || submitted.DiskGiB != 20 {
		t.Fatalf("size preset not resolved correctly: %+v", submitted)
	}
	if submitted.LeaseDays != 14 {
		t.Fatalf("expected default lease 14 days, got %d", submitted.LeaseDays)
	}

	// Verify request is saved in store
	fromStore, ok := cStore.GetRequest(submitted.ID)
	if !ok {
		t.Fatalf("request not found in store")
	}
	if fromStore.Status != StatusPendingApproval {
		t.Fatalf("store status %q != %q", fromStore.Status, StatusPendingApproval)
	}
}

func TestWorkflow_SubmitRequest_NoApproval_AutoProvision(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-auto",
		Name: "Auto Project",
		Members: []project.Member{
			{Kind: "user", Name: "bob", Role: "operator"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:              "bp-auto",
		Name:            "Debian Auto",
		SourceType:      SourceTypeImage,
		SourceRef:       "debian-12.qcow2",
		RequireApproval: false,
		Presets: []SizePreset{
			{ID: "medium", Name: "Medium", VCPUs: 4, MemoryMiB: 4096, DiskGiB: 40},
		},
		DefaultPresetID:  "medium",
		DefaultLeaseDays: 7,
		DefaultSSHKeys:   []string{testSSHKey},
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	dep := &mockDeployer{nextUUID: "auto-prov-vm-uuid"}

	req := Request{
		BlueprintID: "bp-auto",
		ProjectID:   "proj-auto",
		VMName:      "bob-vm-auto",
	}

	submitted, err := SubmitRequest(cStore, pStore, "bob", nil, req, dep)
	if err != nil {
		t.Fatalf("SubmitRequest auto provision: %v", err)
	}

	if submitted.Status != StatusActive {
		t.Fatalf("expected status %q, got %q", StatusActive, submitted.Status)
	}
	if submitted.MachineUUID != "auto-prov-vm-uuid" {
		t.Fatalf("expected machine UUID %q, got %q", "auto-prov-vm-uuid", submitted.MachineUUID)
	}
	if len(dep.createdSpecs) != 1 {
		t.Fatalf("expected 1 created domain, got %d", len(dep.createdSpecs))
	}
	if dep.owners["auto-prov-vm-uuid"] != "bob" {
		t.Fatalf("owner not set: %v", dep.owners)
	}
	if dep.projects["auto-prov-vm-uuid"] != "proj-auto" {
		t.Fatalf("project not set: %v", dep.projects)
	}
}

func TestWorkflow_NonAdminCannotApproveOrReject(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-sec",
		Name: "Security Proj",
		Members: []project.Member{
			{Kind: "user", Name: "alice", Role: "viewer"},
			{Kind: "user", Name: "operator1", Role: "operator"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:              "bp-review",
		Name:            "Review Blueprint",
		SourceType:      SourceTypeImage,
		SourceRef:       "image.qcow2",
		RequireApproval: true,
		Presets: []SizePreset{
			{ID: "p1", Name: "P1", VCPUs: 1, MemoryMiB: 1024, DiskGiB: 10},
		},
		DefaultPresetID: "p1",
		DefaultSSHKeys:  []string{testSSHKey},
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	req, err := SubmitRequest(cStore, pStore, "alice", nil, Request{
		BlueprintID: "bp-review",
		ProjectID:   "proj-sec",
		VMName:      "alice-pending",
	})
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	// operator1 is a project operator (not admin) and global Viewer
	_, err = ApproveRequest(cStore, pStore, req.ID, "operator1", auth.Operator, nil, "try approve")
	if err == nil {
		t.Fatalf("expected unauthorized error for non-admin approve, got nil")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got: %v", err)
	}

	// Check status untouched
	cur, _ := cStore.GetRequest(req.ID)
	if cur.Status != StatusPendingApproval {
		t.Fatalf("status changed to %q after unauthorized approve", cur.Status)
	}

	// Try reject
	_, err = RejectRequest(cStore, pStore, req.ID, "operator1", auth.Operator, nil, "try reject")
	if err == nil {
		t.Fatalf("expected unauthorized error for non-admin reject, got nil")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got: %v", err)
	}

	cur, _ = cStore.GetRequest(req.ID)
	if cur.Status != StatusPendingApproval {
		t.Fatalf("status changed to %q after unauthorized reject", cur.Status)
	}
}

func TestWorkflow_ProjectAdmin_ApproveAndReject(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-team",
		Name: "Team Project",
		Members: []project.Member{
			{Kind: "user", Name: "member1", Role: "viewer"},
			{Kind: "user", Name: "lead", Role: "admin"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:              "bp-team",
		Name:            "Team VM",
		SourceType:      SourceTypeImage,
		SourceRef:       "os.img",
		RequireApproval: true,
		Presets: []SizePreset{
			{ID: "p", Name: "P", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20},
		},
		DefaultPresetID: "p",
		DefaultSSHKeys:  []string{testSSHKey},
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	// 1. Approve
	dep := &mockDeployer{nextUUID: "approved-vm-uuid"}
	req1, err := SubmitRequest(cStore, pStore, "member1", nil, Request{
		BlueprintID: "bp-team",
		ProjectID:   "proj-team",
		VMName:      "req-1",
	})
	if err != nil {
		t.Fatalf("SubmitRequest req1: %v", err)
	}

	// Lead has global Member role (no global admin) but project admin role
	approved, err := ApproveRequest(cStore, pStore, req1.ID, "lead", auth.Member, nil, "Looks good", dep)
	if err != nil {
		t.Fatalf("Project admin failed to approve: %v", err)
	}
	if approved.Status != StatusActive {
		t.Fatalf("expected active status after provisioning, got %q", approved.Status)
	}
	if approved.ReviewedBy != "lead" || approved.ReviewComment != "Looks good" {
		t.Fatalf("review metadata mismatch: %+v", approved)
	}

	// 2. Reject
	req2, err := SubmitRequest(cStore, pStore, "member1", nil, Request{
		BlueprintID: "bp-team",
		ProjectID:   "proj-team",
		VMName:      "req-2",
	})
	if err != nil {
		t.Fatalf("SubmitRequest req2: %v", err)
	}

	rejected, err := RejectRequest(cStore, pStore, req2.ID, "lead", auth.Member, nil, "Quota tight")
	if err != nil {
		t.Fatalf("Project admin failed to reject: %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Fatalf("expected status rejected, got %q", rejected.Status)
	}
	if rejected.ReviewedBy != "lead" || rejected.ReviewComment != "Quota tight" {
		t.Fatalf("review metadata mismatch: %+v", rejected)
	}
}

func TestWorkflow_GlobalAdmin_ApproveAndRejectAny(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-isolated",
		Name: "Isolated",
		Members: []project.Member{
			{Kind: "user", Name: "isolated-user", Role: "operator"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:              "bp-any",
		Name:            "Any VM",
		SourceType:      SourceTypeImage,
		SourceRef:       "os.img",
		RequireApproval: true,
		Presets: []SizePreset{
			{ID: "p", Name: "P", VCPUs: 1, MemoryMiB: 1024, DiskGiB: 10},
		},
		DefaultPresetID: "p",
		DefaultSSHKeys:  []string{testSSHKey},
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	dep := &mockDeployer{nextUUID: "admin-approved-uuid"}
	req, err := SubmitRequest(cStore, pStore, "isolated-user", nil, Request{
		BlueprintID: "bp-any",
		ProjectID:   "proj-isolated",
		VMName:      "iso-vm",
	})
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}

	// root is not a project member, but global role is auth.Admin
	approved, err := ApproveRequest(cStore, pStore, req.ID, "root", auth.Admin, nil, "Global override", dep)
	if err != nil {
		t.Fatalf("Global admin failed to approve: %v", err)
	}
	if approved.Status != StatusActive {
		t.Fatalf("expected status active, got %q", approved.Status)
	}

	// Reject test with another request
	req2, err := SubmitRequest(cStore, pStore, "isolated-user", nil, Request{
		BlueprintID: "bp-any",
		ProjectID:   "proj-isolated",
		VMName:      "iso-vm-2",
	})
	if err != nil {
		t.Fatalf("SubmitRequest req2: %v", err)
	}

	rejected, err := RejectRequest(cStore, pStore, req2.ID, "root", auth.Admin, nil, "Policy violation")
	if err != nil {
		t.Fatalf("Global admin failed to reject: %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Fatalf("expected status rejected, got %q", rejected.Status)
	}
}

func TestWorkflow_SubmitRequest_Validations(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, _ = pStore.Create(project.Project{
		ID:   "proj-val",
		Name: "Validations Project",
		Members: []project.Member{
			{Kind: "user", Name: "charlie", Role: "viewer"},
		},
		Networks: []string{"proj-net"},
	})

	_, _ = cStore.CreateBlueprint(Blueprint{
		ID:              "bp-val",
		Name:            "Validation BP",
		SourceType:      SourceTypeImage,
		SourceRef:       "val.img",
		RequireApproval: true,
		AllowedNetworks: []string{"bp-net", "proj-net"},
		AllowedProjects: []string{"proj-val"},
		Presets: []SizePreset{
			{ID: "tiny", Name: "Tiny", VCPUs: 1, MemoryMiB: 512, DiskGiB: 5},
		},
		DefaultPresetID:  "tiny",
		DefaultLeaseDays: 10,
		MaxLeaseDays:     20,
	})

	// 1. Non-member caller
	_, err := SubmitRequest(cStore, pStore, "outsider", nil, Request{
		BlueprintID: "bp-val",
		ProjectID:   "proj-val",
		VMName:      "vm-out",
	})
	if err == nil || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized error for non-member, got: %v", err)
	}

	// 2. Blueprint not allowed for project
	_, _ = pStore.Create(project.Project{
		ID:   "proj-other",
		Name: "Other Proj",
		Members: []project.Member{
			{Kind: "user", Name: "charlie", Role: "viewer"},
		},
	})
	_, err = SubmitRequest(cStore, pStore, "charlie", nil, Request{
		BlueprintID: "bp-val",
		ProjectID:   "proj-other",
		VMName:      "vm-other",
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed to use blueprint") {
		t.Fatalf("expected disallowed project error, got: %v", err)
	}

	// 3. Lease days exceeds maximum
	_, err = SubmitRequest(cStore, pStore, "charlie", nil, Request{
		BlueprintID: "bp-val",
		ProjectID:   "proj-val",
		VMName:      "vm-lease",
		LeaseDays:   30, // max is 20
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds blueprint maximum") {
		t.Fatalf("expected lease excess error, got: %v", err)
	}

	// 4. Network not allowed by project
	_, err = SubmitRequest(cStore, pStore, "charlie", nil, Request{
		BlueprintID: "bp-val",
		ProjectID:   "proj-val",
		VMName:      "vm-net",
		Network:     "bp-net", // bp allows it, but project only allows proj-net
	})
	if err == nil || !strings.Contains(err.Error(), "not permitted in project") {
		t.Fatalf("expected network restricted by project error, got: %v", err)
	}
}

// TestWorkflow_CloudImageCredentials covers the sign-in credential contract (ADR-0065):
// a cloud-image blueprint refuses an order with no SSH key or password (400), an SSH key
// the user supplies is forwarded to the create path, and a password is stored only as a
// crypt hash that never leaks back through the sanitized API response.
func TestWorkflow_CloudImageCredentials(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-cred",
		Name: "Creds Project",
		Members: []project.Member{
			{Kind: "user", Name: "carol", Role: "operator"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Image blueprint with no default SSH keys: the requester must supply sign-in credentials.
	if _, err := cStore.CreateBlueprint(Blueprint{
		ID:         "bp-img",
		Name:       "Cloud Image BP",
		SourceType: SourceTypeImage,
		SourceRef:  "cloud.img",
		Presets:    []SizePreset{{ID: "s", Name: "S", VCPUs: 1, MemoryMiB: 1024, DiskGiB: 10}},
		DefaultPresetID: "s",
	}); err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	// 1. No credentials -> 400 (ErrBadRequest), before approval or provisioning.
	_, err = SubmitRequest(cStore, pStore, "carol", nil, Request{
		BlueprintID: "bp-img",
		ProjectID:   "proj-cred",
		VMName:      "carol-vm",
		PresetID:    "s",
	})
	if err == nil || !errors.Is(err, ErrBadRequest) {
		t.Fatalf("expected ErrBadRequest for image blueprint without credentials, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cloud image") {
		t.Errorf("expected a cloud-image credential message, got: %v", err)
	}

	dep := &mockDeployer{nextUUID: "cred-vm-uuid"}

	// 2. An SSH key the user supplies is forwarded to the create path.
	submitted, err := SubmitRequest(cStore, pStore, "carol", nil, Request{
		BlueprintID: "bp-img",
		ProjectID:   "proj-cred",
		VMName:      "carol-vm-ssh",
		PresetID:    "s",
		SSHKeys:     []string{testSSHKey},
	}, dep)
	if err != nil {
		t.Fatalf("SubmitRequest with ssh key: %v", err)
	}
	if submitted.Status != StatusActive {
		t.Fatalf("expected status active, got %q", submitted.Status)
	}
	if len(dep.createdSpecs) != 1 {
		t.Fatalf("expected 1 CreateDomain call, got %d", len(dep.createdSpecs))
	}
	ci := dep.createdSpecs[0].CloudInit
	if ci == nil {
		t.Fatal("expected cloud-init on the create spec")
	}
	if len(ci.SSHKeys) != 1 || ci.SSHKeys[0] != testSSHKey {
		t.Errorf("SSH key not forwarded to create path: %v", ci.SSHKeys)
	}
	// An SSH-only order stores no password hash at all.
	if stored, ok := cStore.GetRequest(submitted.ID); ok {
		if stored.Password != "" {
			t.Errorf("plaintext password leaked into the store: %q", stored.Password)
		}
		if stored.PasswordHash != "" {
			t.Errorf("unexpected password hash for an SSH-only order: %q", stored.PasswordHash)
		}
	}

	// 3. A password is stored only as a crypt hash and never returned via Sanitize.
	sub2, err := SubmitRequest(cStore, pStore, "carol", nil, Request{
		BlueprintID: "bp-img",
		ProjectID:   "proj-cred",
		VMName:      "carol-vm-pw",
		PresetID:    "s",
		Password:    "S3cret!pass",
	}, dep)
	if err != nil {
		t.Fatalf("SubmitRequest with password: %v", err)
	}
	stored, ok := cStore.GetRequest(sub2.ID)
	if !ok {
		t.Fatal("request not found in store")
	}
	if stored.Password != "" {
		t.Errorf("plaintext password persisted: %q", stored.Password)
	}
	if stored.PasswordHash == "" {
		t.Fatal("expected the password hash to be persisted")
	}
	if !virt.IsCryptHash(stored.PasswordHash) {
		t.Errorf("stored password is not a crypt hash: %q", stored.PasswordHash)
	}
	if stored.PasswordHash == "S3cret!pass" {
		t.Error("plain password stored instead of a hash")
	}
	// Sanitize strips both the plaintext password and its hash from API responses.
	san := stored.Sanitize()
	if san.Password != "" || san.PasswordHash != "" {
		t.Errorf("sanitized request still carries secrets: %+v", san)
	}
}
