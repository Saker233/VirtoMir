package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default pricing rates for tenant showback calculation.
var DefaultPricing = PricingConfig{
	VCPUHour:     0.05,
	RAMGiBHour:   0.01,
	DiskGiBMonth: 0.10,
	Currency:     "USD",
}

// Standard errors for catalog operations.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("conflict")
	// ErrBadRequest signals a client-side order error that should surface as HTTP 400,
	// e.g. a cloud image blueprint ordered without sign-in credentials.
	ErrBadRequest = errors.New("bad request")
)

type classed struct {
	class error
	msg   string
}

func (e classed) Error() string        { return e.msg }
func (e classed) Is(target error) bool { return target == e.class }
func (e classed) Unwrap() error        { return e.class }

func notFoundf(format string, a ...any) error {
	return classed{ErrNotFound, fmt.Sprintf(format, a...)}
}

func invalidf(format string, a ...any) error {
	return classed{ErrInvalid, fmt.Sprintf(format, a...)}
}

func conflictf(format string, a ...any) error {
	return classed{ErrConflict, fmt.Sprintf(format, a...)}
}

func badRequestf(format string, a ...any) error {
	return classed{ErrBadRequest, fmt.Sprintf(format, a...)}
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type catalogData struct {
	Pricing    PricingConfig `json:"pricing"`
	Blueprints []Blueprint   `json:"blueprints"`
	Requests   []Request     `json:"requests"`
	Leases     []Lease       `json:"leases"`
}

// Store manages persisted catalog blueprints, requests, leases, and pricing rates.
type Store struct {
	path       string
	mu         sync.RWMutex
	blueprints map[string]Blueprint
	requests   map[string]Request
	leases     map[string]Lease
	pricing    PricingConfig
}

// Open loads or initializes the catalog store from the given JSON file.
// If the file does not exist, an empty store with default pricing is initialized.
func Open(path string) (*Store, error) {
	s := &Store{
		path:       path,
		blueprints: make(map[string]Blueprint),
		requests:   make(map[string]Request),
		leases:     make(map[string]Lease),
		pricing:    DefaultPricing,
	}
	if path == "" {
		return s, nil
	}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}

	var data catalogData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if data.Pricing.Currency != "" || data.Pricing.VCPUHour > 0 || data.Pricing.RAMGiBHour > 0 || data.Pricing.DiskGiBMonth > 0 {
		s.pricing = data.Pricing
		if s.pricing.Currency == "" {
			s.pricing.Currency = "USD"
		}
	}

	for _, bp := range data.Blueprints {
		if bp.Presets == nil {
			bp.Presets = []SizePreset{}
		}
		if bp.AllowedNetworks == nil {
			bp.AllowedNetworks = []string{}
		}
		if bp.CloudInitFields == nil {
			bp.CloudInitFields = []InputField{}
		}
		if bp.DefaultSSHKeys == nil {
			bp.DefaultSSHKeys = []string{}
		}
		if bp.AllowedProjects == nil {
			bp.AllowedProjects = []string{}
		}
		s.blueprints[bp.ID] = bp
	}
	for _, req := range data.Requests {
		if req.FieldValues == nil {
			req.FieldValues = map[string]string{}
		}
		s.requests[req.ID] = req
	}
	for _, lease := range data.Leases {
		s.leases[lease.MachineUUID] = lease
	}

	return s, nil
}

// ListBlueprints returns all blueprints, filtered by project if projectID is non-empty, sorted by name.
func (s *Store) ListBlueprints(projectID string) []Blueprint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listBlueprintsLocked(projectID)
}

func (s *Store) listBlueprintsLocked(projectID string) []Blueprint {
	out := make([]Blueprint, 0, len(s.blueprints))
	for _, bp := range s.blueprints {
		if projectID != "" && len(bp.AllowedProjects) > 0 && !slices.Contains(bp.AllowedProjects, projectID) {
			continue
		}
		out = append(out, bp.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GetBlueprint retrieves a blueprint by ID.
func (s *Store) GetBlueprint(id string) (Blueprint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bp, ok := s.blueprints[id]
	if !ok {
		return Blueprint{}, false
	}
	return bp.clone(), true
}

// CreateBlueprint validates and saves a new blueprint.
func (s *Store) CreateBlueprint(bp Blueprint) (Blueprint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bp.ID = strings.TrimSpace(bp.ID)
	bp.Name = strings.TrimSpace(bp.Name)

	if !slugRE.MatchString(bp.ID) {
		return Blueprint{}, invalidf("%q is not a valid blueprint ID (must be a slug matching %s)", bp.ID, slugRE)
	}
	if bp.Name == "" {
		return Blueprint{}, invalidf("blueprint name cannot be empty")
	}
	if bp.SourceType != SourceTypeTemplate && bp.SourceType != SourceTypeImage {
		return Blueprint{}, invalidf("invalid source type %q: must be template or image", bp.SourceType)
	}
	if len(bp.Presets) == 0 {
		return Blueprint{}, invalidf("blueprint requires at least one size preset")
	}

	var defaultFound bool
	for _, p := range bp.Presets {
		if p.ID == bp.DefaultPresetID {
			defaultFound = true
			break
		}
	}
	if bp.DefaultPresetID != "" && !defaultFound {
		return Blueprint{}, invalidf("default preset %q does not match any preset", bp.DefaultPresetID)
	}
	if bp.DefaultPresetID == "" && len(bp.Presets) > 0 {
		bp.DefaultPresetID = bp.Presets[0].ID
	}

	if _, exists := s.blueprints[bp.ID]; exists {
		return Blueprint{}, conflictf("blueprint %q already exists", bp.ID)
	}

	now := time.Now().UTC()
	bp.Created = now
	bp.Updated = now

	saved := bp.clone()
	s.blueprints[bp.ID] = saved
	if err := s.saveLocked(); err != nil {
		delete(s.blueprints, bp.ID)
		return Blueprint{}, err
	}
	return saved.clone(), nil
}

// UpdateBlueprint updates an existing blueprint's configuration.
func (s *Store) UpdateBlueprint(bp Blueprint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.blueprints[bp.ID]
	if !ok {
		return notFoundf("blueprint %q not found", bp.ID)
	}

	name := strings.TrimSpace(bp.Name)
	if name == "" {
		return invalidf("blueprint name cannot be empty")
	}
	if len(bp.Presets) == 0 {
		return invalidf("blueprint requires at least one size preset")
	}
	if bp.SourceType != "" && bp.SourceType != SourceTypeTemplate && bp.SourceType != SourceTypeImage {
		return invalidf("invalid source type %q: must be template or image", bp.SourceType)
	}

	var defaultFound bool
	for _, p := range bp.Presets {
		if p.ID == bp.DefaultPresetID {
			defaultFound = true
			break
		}
	}
	if bp.DefaultPresetID != "" && !defaultFound {
		return invalidf("default preset %q does not match any preset", bp.DefaultPresetID)
	}
	if bp.DefaultPresetID == "" && len(bp.Presets) > 0 {
		bp.DefaultPresetID = bp.Presets[0].ID
	}

	updated := cur.clone()
	updated.Name = name
	updated.Description = bp.Description
	updated.Icon = bp.Icon
	if bp.SourceType != "" {
		updated.SourceType = bp.SourceType
	}
	if bp.SourceRef != "" {
		updated.SourceRef = bp.SourceRef
	}
	if bp.TargetPool != "" {
		updated.TargetPool = bp.TargetPool
	}
	updated.Presets = append([]SizePreset{}, bp.Presets...)
	updated.DefaultPresetID = bp.DefaultPresetID
	if bp.AllowedNetworks != nil {
		updated.AllowedNetworks = append([]string{}, bp.AllowedNetworks...)
	}
	updated.CloudInitTemplate = bp.CloudInitTemplate
	if bp.CloudInitFields != nil {
		updated.CloudInitFields = cloneFields(bp.CloudInitFields)
	}
	if bp.AllowedProjects != nil {
		updated.AllowedProjects = append([]string{}, bp.AllowedProjects...)
	}
	updated.RequireApproval = bp.RequireApproval
	updated.DefaultLeaseDays = bp.DefaultLeaseDays
	updated.MaxLeaseDays = bp.MaxLeaseDays
	updated.Updated = time.Now().UTC()

	s.blueprints[bp.ID] = updated
	if err := s.saveLocked(); err != nil {
		s.blueprints[bp.ID] = cur
		return err
	}
	return nil
}

// DeleteBlueprint removes a blueprint by ID.
func (s *Store) DeleteBlueprint(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.blueprints[id]
	if !ok {
		return notFoundf("blueprint %q not found", id)
	}
	delete(s.blueprints, id)
	if err := s.saveLocked(); err != nil {
		s.blueprints[id] = cur
		return err
	}
	return nil
}

// ListRequests returns order requests matching optional user, projectIDs, and status filters, newest first.
func (s *Store) ListRequests(user string, projectIDs []string, status RequestStatus) []Request {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listRequestsLocked(user, projectIDs, status)
}

func (s *Store) listRequestsLocked(user string, projectIDs []string, status RequestStatus) []Request {
	out := make([]Request, 0, len(s.requests))
	for _, r := range s.requests {
		if user != "" && r.Requester != user {
			continue
		}
		if len(projectIDs) > 0 && !slices.Contains(projectIDs, r.ProjectID) {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		out = append(out, r.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// GetRequest retrieves an order request by ID.
func (s *Store) GetRequest(id string) (Request, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.requests[id]
	if !ok {
		return Request{}, false
	}
	return r.clone(), true
}

// CreateRequest validates and stores a new catalog deployment order.
func (s *Store) CreateRequest(req Request) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req.BlueprintID = strings.TrimSpace(req.BlueprintID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Requester = strings.TrimSpace(req.Requester)
	req.VMName = strings.TrimSpace(req.VMName)

	if req.BlueprintID == "" {
		return Request{}, invalidf("blueprint ID cannot be empty")
	}
	if req.ProjectID == "" {
		return Request{}, invalidf("project ID cannot be empty")
	}
	if req.Requester == "" {
		return Request{}, invalidf("requester cannot be empty")
	}
	if req.VMName == "" {
		return Request{}, invalidf("VM name cannot be empty")
	}

	// Never persist the plaintext password; only its hash may be stored.
	req.Password = ""
	if len(req.SSHKeys) == 0 {
		req.SSHKeys = []string{}
	}

	if req.ID == "" {
		req.ID = randomHex(8)
	}
	if req.Status == "" {
		req.Status = StatusPendingApproval
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now().UTC()
	}

	if _, exists := s.requests[req.ID]; exists {
		return Request{}, conflictf("request %q already exists", req.ID)
	}

	saved := req.clone()
	s.requests[req.ID] = saved
	if err := s.saveLocked(); err != nil {
		delete(s.requests, req.ID)
		return Request{}, err
	}
	return saved.clone(), nil
}

// UpdateRequest updates an existing catalog order request.
func (s *Store) UpdateRequest(req Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.requests[req.ID]
	if !ok {
		return notFoundf("request %q not found", req.ID)
	}

	s.requests[req.ID] = req.clone()
	if err := s.saveLocked(); err != nil {
		s.requests[req.ID] = cur
		return err
	}
	return nil
}

// DeleteRequest deletes an order request by ID.
func (s *Store) DeleteRequest(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.requests[id]
	if !ok {
		return notFoundf("request %q not found", id)
	}
	delete(s.requests, id)
	if err := s.saveLocked(); err != nil {
		s.requests[id] = cur
		return err
	}
	return nil
}

// GetLease retrieves a lease by machine UUID.
func (s *Store) GetLease(machineUUID string) (Lease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.leases[machineUUID]
	if !ok {
		return Lease{}, false
	}
	return l, true
}

// GetLeaseByRequest retrieves a lease associated with a request ID.
func (s *Store) GetLeaseByRequest(requestID string) (Lease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.leases {
		if l.RequestID == requestID {
			return l, true
		}
	}
	return Lease{}, false
}

// ListActiveLeases returns all active, reminder-sent, or grace-period leases, sorted by expiry.
func (s *Store) ListActiveLeases() []Lease {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		if l.State != LeaseStateTerminated {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Expiry.Equal(out[j].Expiry) {
			return out[i].MachineUUID < out[j].MachineUUID
		}
		return out[i].Expiry.Before(out[j].Expiry)
	})
	return out
}

func (s *Store) listLeasesLocked() []Lease {
	out := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].MachineUUID < out[j].MachineUUID
	})
	return out
}

// SaveLease creates or updates a machine lease.
func (s *Store) SaveLease(lease Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(lease.MachineUUID) == "" {
		return invalidf("lease machine UUID cannot be empty")
	}
	if strings.TrimSpace(lease.RequestID) == "" {
		return invalidf("lease request ID cannot be empty")
	}
	if lease.LastStateChange.IsZero() {
		lease.LastStateChange = time.Now().UTC()
	}

	prev, exists := s.leases[lease.MachineUUID]
	s.leases[lease.MachineUUID] = lease
	if err := s.saveLocked(); err != nil {
		if exists {
			s.leases[lease.MachineUUID] = prev
		} else {
			delete(s.leases, lease.MachineUUID)
		}
		return err
	}
	return nil
}

// DeleteLease deletes a lease by machine UUID.
func (s *Store) DeleteLease(machineUUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.leases[machineUUID]
	if !ok {
		return notFoundf("no lease for machine %q", machineUUID)
	}
	delete(s.leases, machineUUID)
	if err := s.saveLocked(); err != nil {
		s.leases[machineUUID] = cur
		return err
	}
	return nil
}

// GetPricing returns the current showback pricing configuration.
func (s *Store) GetPricing() PricingConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pricing
}

// SetPricing validates and saves updated showback unit rates.
func (s *Store) SetPricing(cfg PricingConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg.VCPUHour < 0 || cfg.RAMGiBHour < 0 || cfg.DiskGiBMonth < 0 {
		return invalidf("pricing rates cannot be negative")
	}
	if strings.TrimSpace(cfg.Currency) == "" {
		cfg.Currency = "USD"
	}

	prev := s.pricing
	s.pricing = cfg
	if err := s.saveLocked(); err != nil {
		s.pricing = prev
		return err
	}
	return nil
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data := catalogData{
		Pricing:    s.pricing,
		Blueprints: s.listBlueprintsLocked(""),
		Requests:   s.listRequestsLocked("", nil, ""),
		Leases:     s.listLeasesLocked(),
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := func() error {
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(b); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return err
		}
		return f.Close()
	}(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (bp Blueprint) clone() Blueprint {
	q := bp
	if bp.Presets != nil {
		q.Presets = append([]SizePreset{}, bp.Presets...)
	}
	if bp.AllowedNetworks != nil {
		q.AllowedNetworks = append([]string{}, bp.AllowedNetworks...)
	}
	if bp.CloudInitFields != nil {
		q.CloudInitFields = cloneFields(bp.CloudInitFields)
	}
	if bp.DefaultSSHKeys != nil {
		q.DefaultSSHKeys = append([]string{}, bp.DefaultSSHKeys...)
	}
	if bp.AllowedProjects != nil {
		q.AllowedProjects = append([]string{}, bp.AllowedProjects...)
	}
	return q
}

func cloneFields(fields []InputField) []InputField {
	out := make([]InputField, len(fields))
	for i, f := range fields {
		out[i] = f
		if f.Options != nil {
			out[i].Options = append([]string{}, f.Options...)
		}
	}
	return out
}

func (r Request) clone() Request {
	q := r
	if r.FieldValues != nil {
		q.FieldValues = make(map[string]string, len(r.FieldValues))
		for k, v := range r.FieldValues {
			q.FieldValues[k] = v
		}
	}
	if r.SSHKeys != nil {
		q.SSHKeys = append([]string{}, r.SSHKeys...)
	}
	if r.ReviewedAt != nil {
		t := *r.ReviewedAt
		q.ReviewedAt = &t
	}
	return q
}
