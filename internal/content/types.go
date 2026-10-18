// Package content manages the cluster-wide content library for VirtoMir (ADR-0063).
// It maintains a versioned catalog of VM templates, installation ISOs, and cloud
// images, tracking metadata, SHA-256 checksums, storage pools, and host synchronization
// configurations across cluster nodes.
package content

import "time"

// ItemType classifies a content library item.
type ItemType string

const (
	// TypeTemplate represents a virtual machine template.
	TypeTemplate ItemType = "template"
	// TypeISO represents an installation ISO or bootable image.
	TypeISO ItemType = "iso"
	// TypeImage represents a pre-installed cloud image (e.g. qcow2).
	TypeImage ItemType = "image"
)

// SyncMode determines how an item is synchronized across cluster hosts.
type SyncMode string

const (
	// SyncAll synchronizes this item to all cluster hosts automatically.
	SyncAll SyncMode = "all"
	// SyncSelected synchronizes this item to a specific list of hosts.
	SyncSelected SyncMode = "selected"
	// SyncManual replicates this item only on-demand.
	SyncManual SyncMode = "manual"
)

// SyncPolicy defines replication rules for a content library item.
type SyncPolicy struct {
	Mode  SyncMode `json:"mode"`
	Hosts []string `json:"hosts,omitempty"`
}

// ItemVersion represents a specific immutable version of a library item.
type ItemVersion struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Description string    `json:"description,omitempty"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	Format      string    `json:"format"`
	SourceHost  string    `json:"sourceHost"`
	SourcePool  string    `json:"sourcePool,omitempty"`
	SourceVol   string    `json:"sourceVol,omitempty"`
	TemplateXML string    `json:"templateXml,omitempty"`
	Created     time.Time `json:"created"`
}

// Item represents a catalog entry in the content library.
type Item struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Type         ItemType      `json:"type"`
	Description  string        `json:"description,omitempty"`
	SourceHost   string        `json:"sourceHost"`
	KeepVersions int           `json:"keepVersions"`
	SyncPolicy   SyncPolicy    `json:"syncPolicy"`
	Created      time.Time     `json:"created"`
	Updated      time.Time     `json:"updated"`
	Versions     []ItemVersion `json:"versions"`
}

// HostConfig stores per-host subscription and storage target preferences.
type HostConfig struct {
	HostID     string `json:"hostId"`
	TargetPool string `json:"targetPool"`
	AutoSync   bool   `json:"autoSync"`
}

func (it Item) clone() Item {
	res := it
	if it.SyncPolicy.Hosts != nil {
		res.SyncPolicy.Hosts = append([]string{}, it.SyncPolicy.Hosts...)
	}
	if it.Versions != nil {
		res.Versions = append([]ItemVersion{}, it.Versions...)
	}
	return res
}
