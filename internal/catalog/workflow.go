package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/project"
)

// ErrUnauthorized indicates the user or reviewer lacks appropriate permissions.
var ErrUnauthorized = errors.New("unauthorized")

var (
	defaultDeployerMu sync.RWMutex
	defaultDeployer   Deployer
)

// SetDefaultDeployer registers a package-level fallback deployer for automated provisioning.
func SetDefaultDeployer(d Deployer) {
	defaultDeployerMu.Lock()
	defer defaultDeployerMu.Unlock()
	defaultDeployer = d
}

func getGlobalDeployer() Deployer {
	defaultDeployerMu.RLock()
	defer defaultDeployerMu.RUnlock()
	return defaultDeployer
}

// SubmitRequest validates and submits a catalog order request for a tenant project.
// If the blueprint requires approval, the request is placed into pending_approval.
// If the blueprint does not require approval, it transitions directly to approved and
// proceeds to provisioning if a deployer is provided.
func SubmitRequest(store *Store, projects *project.Store, user string, groups []string, req Request, deployers ...Deployer) (Request, error) {
	if store == nil {
		return Request{}, invalidf("catalog store is not available")
	}
	if projects == nil {
		return Request{}, invalidf("project store is not available")
	}

	req.ProjectID = strings.TrimSpace(req.ProjectID)
	if req.ProjectID == "" {
		return Request{}, invalidf("project ID cannot be empty")
	}
	proj, ok := projects.Get(req.ProjectID)
	if !ok {
		return Request{}, notFoundf("project %q not found", req.ProjectID)
	}

	role, isMember := projects.RoleFor(req.ProjectID, user, groups)
	if !isMember || (role != "viewer" && role != "operator" && role != "admin") {
		return Request{}, fmt.Errorf("%w: user %q is not a member of project %q", ErrUnauthorized, user, req.ProjectID)
	}
	req.Requester = user

	req.VMName = strings.TrimSpace(req.VMName)
	if req.VMName == "" {
		return Request{}, invalidf("VM name cannot be empty")
	}

	req.BlueprintID = strings.TrimSpace(req.BlueprintID)
	if req.BlueprintID == "" {
		return Request{}, invalidf("blueprint ID cannot be empty")
	}
	bp, ok := store.GetBlueprint(req.BlueprintID)
	if !ok {
		return Request{}, notFoundf("blueprint %q not found", req.BlueprintID)
	}

	if len(bp.AllowedProjects) > 0 && !slices.Contains(bp.AllowedProjects, req.ProjectID) {
		return Request{}, invalidf("project %q is not allowed to use blueprint %q", req.ProjectID, bp.ID)
	}

	req.PresetID = strings.TrimSpace(req.PresetID)
	if req.PresetID == "" {
		req.PresetID = bp.DefaultPresetID
	}
	var matchedPreset *SizePreset
	for i := range bp.Presets {
		if bp.Presets[i].ID == req.PresetID {
			matchedPreset = &bp.Presets[i]
			break
		}
	}
	if matchedPreset == nil {
		return Request{}, invalidf("preset %q not found in blueprint %q", req.PresetID, bp.ID)
	}
	req.VCPUs = matchedPreset.VCPUs
	req.MemoryMiB = matchedPreset.MemoryMiB
	req.DiskGiB = matchedPreset.DiskGiB

	// Network validation
	req.Network = strings.TrimSpace(req.Network)
	if req.Network == "" {
		if len(bp.AllowedNetworks) > 0 && len(proj.Networks) > 0 {
			for _, n := range bp.AllowedNetworks {
				if slices.Contains(proj.Networks, n) {
					req.Network = n
					break
				}
			}
		}
		if req.Network == "" && len(bp.AllowedNetworks) > 0 {
			req.Network = bp.AllowedNetworks[0]
		}
		if req.Network == "" && len(proj.Networks) > 0 {
			req.Network = proj.Networks[0]
		}
	}
	if len(bp.AllowedNetworks) > 0 {
		if req.Network == "" || !slices.Contains(bp.AllowedNetworks, req.Network) {
			return Request{}, invalidf("network %q is not permitted by blueprint %q", req.Network, bp.ID)
		}
	}
	if len(proj.Networks) > 0 {
		if req.Network != "" && !slices.Contains(proj.Networks, req.Network) {
			return Request{}, invalidf("network %q is not permitted in project %q", req.Network, proj.ID)
		}
	}

	// Lease validation
	if req.LeaseDays <= 0 {
		req.LeaseDays = bp.DefaultLeaseDays
	}
	if bp.MaxLeaseDays > 0 && req.LeaseDays > bp.MaxLeaseDays {
		return Request{}, invalidf("requested lease duration (%d days) exceeds blueprint maximum (%d days)", req.LeaseDays, bp.MaxLeaseDays)
	}

	if bp.RequireApproval {
		req.Status = StatusPendingApproval
		req.CreatedAt = time.Now().UTC()
		return store.CreateRequest(req)
	}

	req.Status = StatusApproved
	req.CreatedAt = time.Now().UTC()
	saved, err := store.CreateRequest(req)
	if err != nil {
		return Request{}, err
	}

	var dep Deployer
	if len(deployers) > 0 && deployers[0] != nil {
		dep = deployers[0]
	} else if d := getGlobalDeployer(); d != nil {
		dep = d
	}

	if dep != nil {
		_, provErr := Provision(context.Background(), dep, projects, store, saved, nil)
		if updated, ok := store.GetRequest(saved.ID); ok {
			saved = updated
		}
		if provErr != nil {
			return saved, provErr
		}
	}

	return saved, nil
}

// ApproveRequest transitions a pending request to approved if the reviewer is a global admin
// or project admin in the request's project, and triggers machine provisioning.
func ApproveRequest(store *Store, projects *project.Store, requestID, reviewer string, reviewerRole auth.Role, reviewerGroups []string, comment string, deployers ...Deployer) (Request, error) {
	if store == nil {
		return Request{}, invalidf("catalog store is not available")
	}
	if projects == nil {
		return Request{}, invalidf("project store is not available")
	}

	req, ok := store.GetRequest(requestID)
	if !ok {
		return Request{}, notFoundf("request %q not found", requestID)
	}
	if req.Status != StatusPendingApproval {
		return Request{}, invalidf("request %q cannot be approved: current status is %q (expected %q)", requestID, req.Status, StatusPendingApproval)
	}

	isGlobalAdmin := reviewerRole >= auth.Admin
	pRole, _ := projects.RoleFor(req.ProjectID, reviewer, reviewerGroups)
	isProjectAdmin := pRole == "admin"

	if !isGlobalAdmin && !isProjectAdmin {
		return Request{}, fmt.Errorf("%w: reviewer %q must be global admin or project admin in project %q", ErrUnauthorized, reviewer, req.ProjectID)
	}

	now := time.Now().UTC()
	req.Status = StatusApproved
	req.ReviewedAt = &now
	req.ReviewedBy = reviewer
	req.ReviewComment = comment
	if err := store.UpdateRequest(req); err != nil {
		return Request{}, err
	}

	var dep Deployer
	if len(deployers) > 0 && deployers[0] != nil {
		dep = deployers[0]
	} else if d := getGlobalDeployer(); d != nil {
		dep = d
	}

	if dep != nil {
		_, provErr := Provision(context.Background(), dep, projects, store, req, nil)
		if updated, ok := store.GetRequest(req.ID); ok {
			req = updated
		}
		if provErr != nil {
			return req, provErr
		}
	}

	return req, nil
}

// RejectRequest rejects a pending request if the reviewer is a global admin or project admin.
func RejectRequest(store *Store, projects *project.Store, requestID, reviewer string, reviewerRole auth.Role, reviewerGroups []string, comment string) (Request, error) {
	if store == nil {
		return Request{}, invalidf("catalog store is not available")
	}
	if projects == nil {
		return Request{}, invalidf("project store is not available")
	}

	req, ok := store.GetRequest(requestID)
	if !ok {
		return Request{}, notFoundf("request %q not found", requestID)
	}
	if req.Status != StatusPendingApproval {
		return Request{}, invalidf("request %q cannot be rejected: current status is %q (expected %q)", requestID, req.Status, StatusPendingApproval)
	}

	isGlobalAdmin := reviewerRole >= auth.Admin
	pRole, _ := projects.RoleFor(req.ProjectID, reviewer, reviewerGroups)
	isProjectAdmin := pRole == "admin"

	if !isGlobalAdmin && !isProjectAdmin {
		return Request{}, fmt.Errorf("%w: reviewer %q must be global admin or project admin in project %q", ErrUnauthorized, reviewer, req.ProjectID)
	}

	now := time.Now().UTC()
	req.Status = StatusRejected
	req.ReviewedAt = &now
	req.ReviewedBy = reviewer
	req.ReviewComment = comment
	if err := store.UpdateRequest(req); err != nil {
		return Request{}, err
	}

	return req, nil
}
