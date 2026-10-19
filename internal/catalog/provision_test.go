package catalog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func TestProvision_ProjectLimitsExceeded(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	// Create project with explicit limits
	proj := project.Project{
		ID:   "proj-limits",
		Name: "Limits Project",
		Members: []project.Member{
			{Kind: "user", Name: "tester", Role: "admin"},
		},
		Limits: project.Limits{
			Machines:  2,
			VCPUs:     4,
			MemoryMiB: 4096,
			DiskGiB:   50,
		},
	}
	if _, err := pStore.Create(proj); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	bp, err := cStore.CreateBlueprint(Blueprint{
		ID:         "bp-limits",
		Name:       "Limits BP",
		SourceType: SourceTypeImage,
		SourceRef:  "test.qcow2",
		Presets: []SizePreset{
			{ID: "p1", Name: "P1", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20},
		},
		DefaultPresetID: "p1",
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	// 1. Machine count limit exceeded: 2 machines already exist in the project
	depMachines := &mockDeployer{
		domains: []virt.Domain{
			{UUID: "vm-1", Name: "vm-1", Project: "proj-limits", VCPUs: 1, MaxMemoryKiB: 1024 << 10},
			{UUID: "vm-2", Name: "vm-2", Project: "proj-limits", VCPUs: 1, MaxMemoryKiB: 1024 << 10},
		},
	}
	req1, _ := cStore.CreateRequest(Request{
		BlueprintID: bp.ID,
		ProjectID:   "proj-limits",
		Requester:   "tester",
		VMName:      "vm-new-1",
		VCPUs:       1,
		MemoryMiB:   1024,
		DiskGiB:     10,
	})
	_, err = Provision(context.Background(), depMachines, pStore, cStore, req1, nil)
	if err == nil || !strings.Contains(err.Error(), "exceed the project's limit of 2 machines") {
		t.Fatalf("expected machine count limit error, got: %v", err)
	}
	updated1, _ := cStore.GetRequest(req1.ID)
	if updated1.Status != StatusFailed {
		t.Fatalf("expected request status failed, got %q", updated1.Status)
	}

	// 2. VCPU limit exceeded: 1 machine with 3 vCPUs exists, demand is 2 vCPUs (total 5 > 4)
	depVCPU := &mockDeployer{
		domains: []virt.Domain{
			{UUID: "vm-3", Name: "vm-3", Project: "proj-limits", VCPUs: 3, MaxMemoryKiB: 1024 << 10},
		},
	}
	req2, _ := cStore.CreateRequest(Request{
		BlueprintID: bp.ID,
		ProjectID:   "proj-limits",
		Requester:   "tester",
		VMName:      "vm-new-2",
		VCPUs:       2,
		MemoryMiB:   1024,
		DiskGiB:     10,
	})
	_, err = Provision(context.Background(), depVCPU, pStore, cStore, req2, nil)
	if err == nil || !strings.Contains(err.Error(), "exceed the project's limit of 4 vCPUs") {
		t.Fatalf("expected vCPU limit error, got: %v", err)
	}
	updated2, _ := cStore.GetRequest(req2.ID)
	if updated2.Status != StatusFailed {
		t.Fatalf("expected request status failed, got %q", updated2.Status)
	}

	// 3. Memory limit exceeded: 1 machine with 3072 MiB exists, demand is 2048 MiB (total 5120 > 4096)
	depMem := &mockDeployer{
		domains: []virt.Domain{
			{UUID: "vm-4", Name: "vm-4", Project: "proj-limits", VCPUs: 1, MaxMemoryKiB: 3072 << 10},
		},
	}
	req3, _ := cStore.CreateRequest(Request{
		BlueprintID: bp.ID,
		ProjectID:   "proj-limits",
		Requester:   "tester",
		VMName:      "vm-new-3",
		VCPUs:       1,
		MemoryMiB:   2048,
		DiskGiB:     10,
	})
	_, err = Provision(context.Background(), depMem, pStore, cStore, req3, nil)
	if err == nil || !strings.Contains(err.Error(), "exceed the project's limit of 4096 MiB of memory") {
		t.Fatalf("expected memory limit error, got: %v", err)
	}
	updated3, _ := cStore.GetRequest(req3.ID)
	if updated3.Status != StatusFailed {
		t.Fatalf("expected request status failed, got %q", updated3.Status)
	}

	// 4. Disk limit exceeded: existing disk is 40 GiB (via active requests in store), demand is 20 GiB (total 60 > 50)
	depDisk := &mockDeployer{
		domains: []virt.Domain{
			{UUID: "vm-5", Name: "vm-5", Project: "proj-limits", VCPUs: 1, MaxMemoryKiB: 512 << 10},
		},
	}
	// Record existing active request in store taking 40 GiB for vm-5
	_, _ = cStore.CreateRequest(Request{
		ID:          "existing-active-req",
		BlueprintID: bp.ID,
		ProjectID:   "proj-limits",
		Requester:   "tester",
		VMName:      "vm-5",
		MachineUUID: "vm-5",
		Status:      StatusActive,
		DiskGiB:     40,
	})
	req4, _ := cStore.CreateRequest(Request{
		BlueprintID: bp.ID,
		ProjectID:   "proj-limits",
		Requester:   "tester",
		VMName:      "vm-new-4",
		VCPUs:       1,
		MemoryMiB:   512,
		DiskGiB:     20,
	})
	_, err = Provision(context.Background(), depDisk, pStore, cStore, req4, nil)
	if err == nil || !strings.Contains(err.Error(), "exceed the project's limit of 50 GiB of disk space") {
		t.Fatalf("expected disk limit error, got: %v", err)
	}
	updated4, _ := cStore.GetRequest(req4.ID)
	if updated4.Status != StatusFailed {
		t.Fatalf("expected request status failed, got %q", updated4.Status)
	}
}

func TestProvision_CloudInitSubstitution(t *testing.T) {
	tmpl := `#cloud-config
hostname: {{.Hostname}}
users:
  - name: {{.User}}
    plain_text_passwd: {{.Password}}
    ssh_authorized_keys:
      - {{.SSHKey}}
env: {{.AppEnv}}
`
	fieldValues := map[string]string{
		"User":     "devops",
		"Password": "Secret123Password!",
		"SSHKey":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey",
		"Hostname": "ci-test-node",
		"AppEnv":   "production",
	}

	defaults := map[string]string{
		"Hostname": "default-host",
		"User":     "default-user",
	}

	rendered, err := RenderCloudInitTemplate(tmpl, fieldValues, defaults)
	if err != nil {
		t.Fatalf("RenderCloudInitTemplate: %v", err)
	}

	if !strings.Contains(rendered, "hostname: ci-test-node") {
		t.Errorf("expected hostname: ci-test-node, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- name: devops") {
		t.Errorf("expected user devops, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "plain_text_passwd: Secret123Password!") {
		t.Errorf("expected password in rendered output, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "- ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey") {
		t.Errorf("expected ssh key in rendered output, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "env: production") {
		t.Errorf("expected env: production, got:\n%s", rendered)
	}

	// Test BuildCloudInit
	bp := Blueprint{
		ID:                "bp-ci",
		CloudInitTemplate: tmpl,
	}
	req := Request{
		VMName:      "ci-test-node",
		Requester:   "admin",
		FieldValues: fieldValues,
	}
	ci, err := BuildCloudInit(bp, req)
	if err != nil {
		t.Fatalf("BuildCloudInit: %v", err)
	}
	if ci == nil {
		t.Fatalf("expected non-nil CloudInit struct")
	}
	if ci.Hostname != "ci-test-node" {
		t.Errorf("expected ci.Hostname ci-test-node, got %q", ci.Hostname)
	}
	if ci.User != "devops" {
		t.Errorf("expected ci.User devops, got %q", ci.User)
	}
	if ci.Password != "Secret123Password!" {
		t.Errorf("expected ci.Password Secret123Password!, got %q", ci.Password)
	}
	if len(ci.SSHKeys) != 1 || ci.SSHKeys[0] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey" {
		t.Errorf("expected SSHKeys set, got: %v", ci.SSHKeys)
	}
	if !strings.HasPrefix(ci.UserData, "#cloud-config") {
		t.Errorf("expected UserData to start with #cloud-config, got:\n%s", ci.UserData)
	}
}

func TestProvision_Successful_CreatesActiveLease(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-lease",
		Name: "Lease Project",
		Members: []project.Member{
			{Kind: "user", Name: "dave", Role: "operator"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:         "bp-lease",
		Name:       "Lease BP",
		SourceType: SourceTypeImage,
		SourceRef:  "debian.qcow2",
		Presets: []SizePreset{
			{ID: "std", Name: "Standard", VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20},
		},
		DefaultPresetID:  "std",
		DefaultLeaseDays: 14,
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	dep := &mockDeployer{nextUUID: "lease-vm-uuid-99"}

	req, err := cStore.CreateRequest(Request{
		BlueprintID: "bp-lease",
		ProjectID:   "proj-lease",
		Requester:   "dave",
		VMName:      "dave-server",
		PresetID:    "std",
		VCPUs:       2,
		MemoryMiB:   2048,
		DiskGiB:     20,
		LeaseDays:   7,
		Status:      StatusApproved,
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	before := time.Now().UTC()
	uuid, err := Provision(context.Background(), dep, pStore, cStore, req, nil)
	if err != nil {
		t.Fatalf("Provision failed: %v", err)
	}
	after := time.Now().UTC()

	if uuid != "lease-vm-uuid-99" {
		t.Fatalf("expected UUID lease-vm-uuid-99, got %q", uuid)
	}

	// Verify request updated to active
	upReq, ok := cStore.GetRequest(req.ID)
	if !ok {
		t.Fatalf("request not found in store")
	}
	if upReq.Status != StatusActive {
		t.Fatalf("expected request status active, got %q", upReq.Status)
	}
	if upReq.MachineUUID != "lease-vm-uuid-99" {
		t.Fatalf("expected request machine UUID lease-vm-uuid-99, got %q", upReq.MachineUUID)
	}

	// Verify lease created and active with ~7 days expiry
	lease, ok := cStore.GetLease("lease-vm-uuid-99")
	if !ok {
		t.Fatalf("lease not found in store")
	}
	if lease.State != LeaseStateActive {
		t.Fatalf("expected lease state active, got %q", lease.State)
	}
	if lease.ProjectID != "proj-lease" {
		t.Fatalf("expected lease project proj-lease, got %q", lease.ProjectID)
	}
	if lease.RequestID != req.ID {
		t.Fatalf("expected lease request ID %q, got %q", req.ID, lease.RequestID)
	}

	expectedMin := before.Add(7 * 24 * time.Hour)
	expectedMax := after.Add(7 * 24 * time.Hour)
	if lease.Expiry.Before(expectedMin) || lease.Expiry.After(expectedMax) {
		t.Fatalf("lease expiry %v not within expected range [%v, %v]", lease.Expiry, expectedMin, expectedMax)
	}

	// Verify deployer calls
	if dep.owners["lease-vm-uuid-99"] != "dave" {
		t.Errorf("expected owner dave, got %q", dep.owners["lease-vm-uuid-99"])
	}
	if dep.projects["lease-vm-uuid-99"] != "proj-lease" {
		t.Errorf("expected project proj-lease, got %q", dep.projects["lease-vm-uuid-99"])
	}
	if len(dep.createdSpecs) != 1 {
		t.Fatalf("expected 1 CreateDomain call, got %d", len(dep.createdSpecs))
	}
	cs := dep.createdSpecs[0]
	if cs.Name != "dave-server" || cs.VCPUs != 2 || cs.MemoryMiB != 2048 || cs.DiskGiB != 20 {
		t.Errorf("CreateSpec attributes mismatch: %+v", cs)
	}
}

func TestProvision_TemplateSourceType(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, err := pStore.Create(project.Project{
		ID:   "proj-tmpl",
		Name: "Template Project",
		Members: []project.Member{
			{Kind: "user", Name: "emma", Role: "operator"},
		},
	})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	_, err = cStore.CreateBlueprint(Blueprint{
		ID:         "bp-tmpl",
		Name:       "Template Blueprint",
		SourceType: SourceTypeTemplate,
		SourceRef:  "golden-image-uuid",
		Presets: []SizePreset{
			{ID: "tmpl-large", Name: "Large", VCPUs: 8, MemoryMiB: 16384, DiskGiB: 100},
		},
		DefaultPresetID: "tmpl-large",
	})
	if err != nil {
		t.Fatalf("Create blueprint: %v", err)
	}

	dep := &mockDeployer{nextUUID: "deployed-from-tmpl-uuid"}

	req, _ := cStore.CreateRequest(Request{
		BlueprintID: "bp-tmpl",
		ProjectID:   "proj-tmpl",
		Requester:   "emma",
		VMName:      "emma-tmpl-vm",
		VCPUs:       8,
		MemoryMiB:   16384,
		DiskGiB:     100,
		Status:      StatusApproved,
	})

	uuid, err := Provision(context.Background(), dep, pStore, cStore, req, nil)
	if err != nil {
		t.Fatalf("Provision from template failed: %v", err)
	}

	if uuid != "deployed-from-tmpl-uuid" {
		t.Fatalf("expected deployed-from-tmpl-uuid, got %q", uuid)
	}
	if len(dep.deploySpecs) != 1 {
		t.Fatalf("expected 1 Deploy call, got %d", len(dep.deploySpecs))
	}
	ds := dep.deploySpecs[0]
	if ds.Name != "emma-tmpl-vm" || ds.VCPUs != 8 || ds.MemoryMiB != 16384 {
		t.Errorf("DeploySpec overrides mismatch: %+v", ds)
	}
	if dep.owners[uuid] != "emma" || dep.projects[uuid] != "proj-tmpl" {
		t.Errorf("metadata tagging mismatch: owner=%q project=%q", dep.owners[uuid], dep.projects[uuid])
	}
}

func TestProvision_StoragePoolAndNetworkRestrictions(t *testing.T) {
	cStore, pStore := setupTestStores(t)

	_, _ = pStore.Create(project.Project{
		ID:   "proj-restrict",
		Name: "Restricted",
		Members: []project.Member{
			{Kind: "user", Name: "frank", Role: "admin"},
		},
		StoragePools: []string{"fast-nvme"},
		Networks:     []string{"corp-lan"},
	})

	// 1. TargetPool not in project allowed pools
	_, _ = cStore.CreateBlueprint(Blueprint{
		ID:         "bp-wrong-pool",
		Name:       "Wrong Pool BP",
		SourceType: SourceTypeImage,
		SourceRef:  "img.qcow2",
		TargetPool: "slow-hdd", // project only allows fast-nvme
		Presets: []SizePreset{
			{ID: "s", Name: "S", VCPUs: 1, MemoryMiB: 1024, DiskGiB: 10},
		},
		DefaultPresetID: "s",
	})

	dep := &mockDeployer{}
	req1, _ := cStore.CreateRequest(Request{
		BlueprintID: "bp-wrong-pool",
		ProjectID:   "proj-restrict",
		Requester:   "frank",
		VMName:      "test-pool",
		Status:      StatusApproved,
	})
	_, err := Provision(context.Background(), dep, pStore, cStore, req1, nil)
	if err == nil || !strings.Contains(err.Error(), "storage pool \"slow-hdd\" is not allowed") {
		t.Fatalf("expected storage pool restriction error, got: %v", err)
	}

	// 2. Network not in project allowed networks
	_, _ = cStore.CreateBlueprint(Blueprint{
		ID:         "bp-wrong-net",
		Name:       "Wrong Net BP",
		SourceType: SourceTypeImage,
		SourceRef:  "img.qcow2",
		TargetPool: "fast-nvme",
		Presets: []SizePreset{
			{ID: "s", Name: "S", VCPUs: 1, MemoryMiB: 1024, DiskGiB: 10},
		},
		DefaultPresetID: "s",
	})
	req2, _ := cStore.CreateRequest(Request{
		BlueprintID: "bp-wrong-net",
		ProjectID:   "proj-restrict",
		Requester:   "frank",
		VMName:      "test-net",
		Network:     "guest-wifi", // not allowed in proj-restrict
		Status:      StatusApproved,
	})
	_, err = Provision(context.Background(), dep, pStore, cStore, req2, nil)
	if err == nil || !strings.Contains(err.Error(), "network \"guest-wifi\" is not allowed") {
		t.Fatalf("expected network restriction error, got: %v", err)
	}
}
