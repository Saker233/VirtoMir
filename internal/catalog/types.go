package catalog

import "time"

// SourceType defines whether a blueprint originates from a VM template or a cloud image.
type SourceType string

const (
	SourceTypeTemplate SourceType = "template"
	SourceTypeImage    SourceType = "image"
)

// SizePreset defines a compute and disk size offering for blueprint deployments.
type SizePreset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB uint64 `json:"memoryMiB"`
	DiskGiB   uint64 `json:"diskGiB"`
}

// InputField defines a customizable user parameter filled during catalog order.
type InputField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // "string", "password", "ssh_key", "number", "select"
	Required bool     `json:"required"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
}

// Blueprint defines a catalog item deployable into projects.
type Blueprint struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Description       string       `json:"description,omitempty"`
	Icon              string       `json:"icon,omitempty"`
	SourceType        SourceType   `json:"sourceType"`
	SourceRef         string       `json:"sourceRef"`
	TargetPool        string       `json:"targetPool,omitempty"`
	Presets           []SizePreset `json:"presets"`
	DefaultPresetID   string       `json:"defaultPresetId,omitempty"`
	AllowedNetworks   []string     `json:"allowedNetworks,omitempty"`
	CloudInitTemplate string       `json:"cloudInitTemplate,omitempty"`
	CloudInitFields   []InputField `json:"cloudInitFields,omitempty"`
	AllowedProjects   []string     `json:"allowedProjects,omitempty"`
	RequireApproval   bool         `json:"requireApproval"`
	DefaultLeaseDays  int          `json:"defaultLeaseDays,omitempty"`
	MaxLeaseDays      int          `json:"maxLeaseDays,omitempty"`
	Created           time.Time    `json:"created"`
	Updated           time.Time    `json:"updated"`
}

// RequestStatus represents the lifecycle state of a catalog order request.
type RequestStatus string

const (
	StatusPendingApproval RequestStatus = "pending_approval"
	StatusApproved        RequestStatus = "approved"
	StatusRejected        RequestStatus = "rejected"
	StatusProvisioning    RequestStatus = "provisioning"
	StatusActive          RequestStatus = "active"
	StatusExpired         RequestStatus = "expired"
	StatusFailed          RequestStatus = "failed"
)

// Request defines an order placed by a tenant user for a blueprint deployment.
type Request struct {
	ID            string            `json:"id"`
	BlueprintID   string            `json:"blueprintId"`
	ProjectID     string            `json:"projectId"`
	Requester     string            `json:"requester"`
	VMName        string            `json:"vmName"`
	PresetID      string            `json:"presetId"`
	VCPUs         int               `json:"vcpus"`
	MemoryMiB     uint64            `json:"memoryMiB"`
	DiskGiB       uint64            `json:"diskGiB"`
	Network       string            `json:"network,omitempty"`
	FieldValues   map[string]string `json:"fieldValues,omitempty"`
	LeaseDays     int               `json:"leaseDays"`
	Status        RequestStatus     `json:"status"`
	CreatedAt     time.Time         `json:"createdAt"`
	ReviewedAt    *time.Time        `json:"reviewedAt,omitempty"`
	ReviewedBy    string            `json:"reviewedBy,omitempty"`
	ReviewComment string            `json:"reviewComment,omitempty"`
	MachineUUID   string            `json:"machineUuid,omitempty"`
	ErrorMessage  string            `json:"errorMessage,omitempty"`
}

// LeaseState represents the lifecycle phase of a provisioned machine lease.
type LeaseState string

const (
	LeaseStateActive       LeaseState = "active"
	LeaseStateReminderSent LeaseState = "reminder_sent"
	LeaseStateGracePeriod  LeaseState = "grace_period"
	LeaseStateTerminated   LeaseState = "terminated"
)

// Lease tracks the expiration and lifecycle of a provisioned catalog machine.
type Lease struct {
	RequestID       string     `json:"requestId"`
	MachineUUID     string     `json:"machineUuid"`
	ProjectID       string     `json:"projectId"`
	Expiry          time.Time  `json:"expiry"`
	GraceExpiry     time.Time  `json:"graceExpiry,omitempty"`
	State           LeaseState `json:"state"`
	ReminderSent    bool       `json:"reminderSent"`
	LastStateChange time.Time  `json:"lastStateChange"`
}

// PricingConfig configures unit costs for tenant showback reporting.
type PricingConfig struct {
	VCPUHour     float64 `json:"vcpuHour"`
	RAMGiBHour   float64 `json:"ramGiBHour"`
	DiskGiBMonth float64 `json:"diskGiBMonth"`
	Currency     string  `json:"currency"`
}
