package catalog

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/onurbaydas/virtomir/internal/project"
	"github.com/onurbaydas/virtomir/internal/virt"
)

// Deployer executes VM lifecycle operations in the hypervisor.
type Deployer interface {
	CreateDomain(spec virt.CreateSpec) (virt.CreateResult, error)
	Deploy(ctx context.Context, template string, spec virt.DeploySpec, report func(string)) (string, error)
	SetOwner(uuid, owner string) error
	SetProject(uuid, project string) error
	ListDomains() ([]virt.Domain, error)
}

// RenderCloudInitTemplate parses and executes a cloud-init template string with field values and defaults.
func RenderCloudInitTemplate(tmplStr string, fieldValues map[string]string, defaults map[string]string) (string, error) {
	data := make(map[string]any)
	for k, v := range defaults {
		data[k] = v
		if len(k) > 0 {
			data[strings.ToUpper(k[:1])+k[1:]] = v
			data[strings.ToLower(k)] = v
		}
	}
	for k, v := range fieldValues {
		data[k] = v
		if len(k) > 0 {
			data[strings.ToUpper(k[:1])+k[1:]] = v
			data[strings.ToLower(k)] = v
		}
		if strings.EqualFold(k, "sshkey") || strings.EqualFold(k, "ssh_key") {
			data["SSHKey"] = v
			data["sshKey"] = v
			data["ssh_key"] = v
		}
	}

	tmpl, err := template.New("cloudinit").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// BuildCloudInit constructs a virt.CloudInit struct from blueprint template and user field values.
func BuildCloudInit(bp Blueprint, req Request) (*virt.CloudInit, error) {
	ciUser := ""
	ciPassword := ""
	ciSSHKey := ""
	ciHostname := req.VMName

	for k, v := range req.FieldValues {
		switch strings.ToLower(k) {
		case "user", "username":
			ciUser = v
		case "password":
			ciPassword = v
		case "ssh_key", "sshkey", "ssh-key":
			ciSSHKey = v
		case "hostname":
			ciHostname = v
		}
	}
	if ciHostname == "" {
		ciHostname = req.VMName
	}
	if ciUser == "" {
		ciUser = req.Requester
	}

	defaults := map[string]string{
		"Hostname": ciHostname,
		"VMName":   req.VMName,
		"User":     ciUser,
		"Password": ciPassword,
		"SSHKey":   ciSSHKey,
		"Project":  req.ProjectID,
	}

	var rendered string
	if strings.TrimSpace(bp.CloudInitTemplate) != "" {
		out, err := RenderCloudInitTemplate(bp.CloudInitTemplate, req.FieldValues, defaults)
		if err != nil {
			return nil, fmt.Errorf("render cloud-init template: %w", err)
		}
		rendered = out
	}

	if strings.TrimSpace(rendered) == "" && ciUser == "" && ciPassword == "" && ciSSHKey == "" {
		return nil, nil
	}

	ci := &virt.CloudInit{
		Hostname: ciHostname,
		User:     ciUser,
		Password: ciPassword,
	}
	if ciSSHKey != "" {
		ci.SSHKeys = []string{ciSSHKey}
	}
	if rendered != "" {
		trimmed := strings.TrimSpace(rendered)
		if !strings.HasPrefix(trimmed, "#cloud-config") && !strings.HasPrefix(trimmed, "#!") {
			rendered = "#cloud-config\n" + rendered
		}
		ci.UserData = rendered
	}
	return ci, nil
}

// Provision executes automated machine deployment for an approved catalog request:
// enforces project resource limits, validates network and pool permissions, renders
// cloud-init configs, creates the domain, tags ownership/project, and records the lease.
func Provision(ctx context.Context, deployer Deployer, projects *project.Store, store *Store, req Request, report func(string)) (string, error) {
	if report == nil {
		report = func(string) {}
	}

	fail := func(err error) (string, error) {
		req.Status = StatusFailed
		req.ErrorMessage = err.Error()
		if store != nil {
			_ = store.UpdateRequest(req)
		}
		return "", err
	}

	// 1. Mark request.Status = "provisioning" in store.
	req.Status = StatusProvisioning
	if store != nil {
		if err := store.UpdateRequest(req); err != nil {
			return fail(fmt.Errorf("update request status to provisioning: %w", err))
		}
	}

	if deployer == nil {
		return fail(fmt.Errorf("deployer is nil"))
	}
	if projects == nil {
		return fail(fmt.Errorf("project store is nil"))
	}
	if store == nil {
		return fail(fmt.Errorf("catalog store is nil"))
	}

	// Resolve blueprint
	bp, ok := store.GetBlueprint(req.BlueprintID)
	if !ok {
		return fail(fmt.Errorf("blueprint %q not found", req.BlueprintID))
	}

	// Load project
	proj, ok := projects.Get(req.ProjectID)
	if !ok {
		return fail(fmt.Errorf("project %q not found", req.ProjectID))
	}

	// Compute project usage
	doms, err := deployer.ListDomains()
	if err != nil {
		return fail(fmt.Errorf("list domains: %w", err))
	}

	var u project.Usage
	var mine []string
	for _, d := range doms {
		if d.Project == req.ProjectID {
			u.Machines++
			u.VCPUs += d.VCPUs
			u.MemoryMiB += int64(d.MaxMemoryKiB >> 10)
			mine = append(mine, d.UUID)
		}
	}

	// Compute disk usage
	var diskGiB int64
	if sizer, ok := deployer.(interface{ DiskBytes([]string) (map[string]uint64, error) }); ok {
		if sizes, err := sizer.DiskBytes(mine); err == nil {
			var totalBytes uint64
			for _, b := range sizes {
				totalBytes += b
			}
			diskGiB = int64((totalBytes + 1<<30 - 1) >> 30)
		}
	} else if sizer, ok := deployer.(interface{ DiskGiB([]string) uint64 }); ok {
		diskGiB = int64(sizer.DiskGiB(mine))
	}
	if diskGiB == 0 {
		for _, reqItem := range store.ListRequests("", []string{req.ProjectID}, "") {
			if (reqItem.Status == StatusActive || reqItem.Status == StatusProvisioning) && slices.Contains(mine, reqItem.MachineUUID) {
				diskGiB += int64(reqItem.DiskGiB)
			}
		}
	}
	u.DiskGiB = diskGiB

	// Check project limits
	demand := project.Usage{
		Machines:  1,
		VCPUs:     req.VCPUs,
		MemoryMiB: int64(req.MemoryMiB),
		DiskGiB:   int64(req.DiskGiB),
	}
	if err := proj.Limits.Exceeded(u, demand); err != nil {
		return fail(err)
	}

	// Check project.StoragePools and project.Networks allowed lists
	pool := bp.TargetPool
	if pool == "" && len(proj.StoragePools) > 0 {
		pool = proj.StoragePools[0]
	}
	if pool == "" {
		pool = "default"
	}
	if len(proj.StoragePools) > 0 && !slices.Contains(proj.StoragePools, pool) {
		return fail(fmt.Errorf("storage pool %q is not allowed in project %q", pool, proj.ID))
	}

	network := req.Network
	if network == "" {
		if len(bp.AllowedNetworks) > 0 && len(proj.Networks) > 0 {
			for _, n := range bp.AllowedNetworks {
				if slices.Contains(proj.Networks, n) {
					network = n
					break
				}
			}
		}
		if network == "" && len(bp.AllowedNetworks) > 0 {
			network = bp.AllowedNetworks[0]
		}
		if network == "" && len(proj.Networks) > 0 {
			network = proj.Networks[0]
		}
	}
	if len(bp.AllowedNetworks) > 0 && network != "" && !slices.Contains(bp.AllowedNetworks, network) {
		return fail(fmt.Errorf("network %q is not in blueprint allowed networks", network))
	}
	if len(proj.Networks) > 0 && network != "" && !slices.Contains(proj.Networks, network) {
		return fail(fmt.Errorf("network %q is not allowed in project %q", network, proj.ID))
	}
	req.Network = network

	// Cloud-Init generation
	ci, err := BuildCloudInit(bp, req)
	if err != nil {
		return fail(fmt.Errorf("cloud-init generation failed: %w", err))
	}

	// Provision machine
	var uuid string
	switch bp.SourceType {
	case SourceTypeTemplate:
		report("Deploying machine from template " + bp.SourceRef + "…")
		deploySpec := virt.DeploySpec{
			Name:      req.VMName,
			CloudInit: ci,
			Start:     true,
			VCPUs:     req.VCPUs,
			MemoryMiB: req.MemoryMiB,
		}
		resUUID, err := deployer.Deploy(ctx, bp.SourceRef, deploySpec, report)
		if err != nil {
			return fail(fmt.Errorf("deploy from template: %w", err))
		}
		uuid = resUUID

	case SourceTypeImage:
		report("Creating machine from image " + bp.SourceRef + "…")
		var nic *virt.NICSpec
		if req.Network != "" {
			nic = &virt.NICSpec{Network: req.Network}
		}
		createSpec := virt.CreateSpec{
			Name:      req.VMName,
			VCPUs:     req.VCPUs,
			MemoryMiB: req.MemoryMiB,
			DiskGiB:   req.DiskGiB,
			Pool:      pool,
			BaseImage: bp.SourceRef,
			Network:   nic,
			CloudInit: ci,
			Start:     true,
		}
		res, err := deployer.CreateDomain(createSpec)
		if err != nil {
			return fail(fmt.Errorf("create domain: %w", err))
		}
		uuid = res.UUID

	default:
		return fail(fmt.Errorf("unsupported blueprint source type %q", bp.SourceType))
	}

	if uuid == "" {
		return fail(fmt.Errorf("provisioning produced an empty machine UUID"))
	}

	// Records ownership and project
	if err := deployer.SetOwner(uuid, req.Requester); err != nil {
		return fail(fmt.Errorf("set owner: %w", err))
	}
	if err := deployer.SetProject(uuid, req.ProjectID); err != nil {
		return fail(fmt.Errorf("set project: %w", err))
	}

	// Lease registration: if req.LeaseDays > 0, creates and saves Lease
	leaseDays := req.LeaseDays
	if leaseDays <= 0 && bp.DefaultLeaseDays > 0 {
		leaseDays = bp.DefaultLeaseDays
	}
	if leaseDays > 0 {
		now := time.Now().UTC()
		lease := Lease{
			RequestID:       req.ID,
			MachineUUID:     uuid,
			ProjectID:       req.ProjectID,
			Expiry:          now.Add(time.Duration(leaseDays) * 24 * time.Hour),
			State:           LeaseStateActive,
			ReminderSent:    false,
			LastStateChange: now,
		}
		if err := store.SaveLease(lease); err != nil {
			return fail(fmt.Errorf("save lease: %w", err))
		}
	}

	// Updates request: req.Status = "active", req.MachineUUID = uuid, saves to store.
	req.Status = StatusActive
	req.MachineUUID = uuid
	if err := store.UpdateRequest(req); err != nil {
		return fail(fmt.Errorf("update request status: %w", err))
	}

	report("Machine " + req.VMName + " (" + uuid + ") provisioned successfully")
	return uuid, nil
}
