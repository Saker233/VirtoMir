// Package overlay keeps the host's VXLAN tunnel devices in line with VirtoMir's VXLAN
// networks (ADR-0053). libvirt creates each network's bridge; for every active one this
// package makes sure a vmvx<VNI> device exists, is attached to that bridge and floods
// broadcast and unknown traffic to each peer host (head-end replication through all-zero
// forwarding entries). Devices whose network is gone or stopped are removed. Tunnels only
// touch their own bridges, never the host's uplink, so they need no confirm-or-roll-back
// like host interfaces do; they are not persisted either: virtomird recreates them.
package overlay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Port is the IANA VXLAN port; the hosts' firewalls must let UDP to it through.
const Port = 4789

// MTU leaves room for the 50-byte VXLAN header on a 1500-byte underlay; the networks'
// bridges use the same.
const MTU = 1450

// Prefix names VirtoMir's tunnel devices; others are left alone.
const Prefix = "vmvx"

// Device is the tunnel device of vni.
func Device(vni uint) string { return Prefix + strconv.FormatUint(uint64(vni), 10) }

// Tunnel is what one active VXLAN network needs.
type Tunnel struct {
	VNI    uint
	Bridge string
	Peers  []netip.Addr
}

// Runner runs ip and bridge (tests replace it).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Reconciler applies the wanted tunnels.
type Reconciler struct {
	run Runner
	log *slog.Logger
}

func New(r Runner, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{run: r, log: log}
}

type link struct {
	Name     string   `json:"ifname"`
	Master   string   `json:"master"`
	MTU      int      `json:"mtu"`
	Flags    []string `json:"flags"`
	LinkInfo struct {
		Data struct {
			ID   uint `json:"id"`
			Port int  `json:"port"`
		} `json:"info_data"`
	} `json:"linkinfo"`
}

type fdb struct {
	MAC string `json:"mac"`
	Dst string `json:"dst"`
}

// Reconcile makes the host's vmvx devices match want. It carries on past a failing tunnel
// and returns the errors joined.
func (r *Reconciler) Reconcile(ctx context.Context, want []Tunnel) error {
	out, err := r.run.Run(ctx, "ip", "-j", "-d", "link", "show", "type", "vxlan")
	if err != nil {
		return fmt.Errorf("list vxlan devices: %w", err)
	}
	var links []link
	if s := strings.TrimSpace(string(out)); s != "" {
		if err := json.Unmarshal([]byte(s), &links); err != nil {
			return fmt.Errorf("list vxlan devices: %w", err)
		}
	}
	have := map[string]link{}
	for _, l := range links {
		if strings.HasPrefix(l.Name, Prefix) {
			have[l.Name] = l
		}
	}
	wanted := map[string]Tunnel{}
	for _, t := range want {
		wanted[Device(t.VNI)] = t
	}

	var errs []error
	for name, l := range have {
		t, ok := wanted[name]
		if ok && l.LinkInfo.Data.ID == t.VNI && l.LinkInfo.Data.Port == Port {
			continue
		}
		if _, err := r.run.Run(ctx, "ip", "link", "del", name); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", name, err))
			continue
		}
		r.log.Info("vxlan tunnel removed", "device", name)
		delete(have, name)
	}
	for _, name := range sortedKeys(wanted) {
		if err := r.apply(ctx, wanted[name], have); err != nil {
			errs = append(errs, fmt.Errorf("VNI %d: %w", wanted[name].VNI, err))
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) apply(ctx context.Context, t Tunnel, have map[string]link) error {
	dev := Device(t.VNI)
	l, exists := have[dev]
	if !exists {
		if _, err := r.run.Run(ctx, "ip", "link", "add", dev, "mtu", strconv.Itoa(MTU), "type", "vxlan",
			"id", strconv.FormatUint(uint64(t.VNI), 10), "dstport", strconv.Itoa(Port)); err != nil {
			return fmt.Errorf("create %s: %w", dev, err)
		}
		r.log.Info("vxlan tunnel created", "device", dev, "bridge", t.Bridge, "peers", len(t.Peers))
	}
	if exists && l.MTU != MTU {
		if _, err := r.run.Run(ctx, "ip", "link", "set", dev, "mtu", strconv.Itoa(MTU)); err != nil {
			return fmt.Errorf("set the MTU of %s: %w", dev, err)
		}
	}
	if l.Master != t.Bridge {
		if _, err := r.run.Run(ctx, "ip", "link", "set", dev, "master", t.Bridge); err != nil {
			return fmt.Errorf("attach %s to %s: %w", dev, t.Bridge, err)
		}
	}
	if !exists || !slices.Contains(l.Flags, "UP") {
		if _, err := r.run.Run(ctx, "ip", "link", "set", dev, "up"); err != nil {
			return fmt.Errorf("bring %s up: %w", dev, err)
		}
	}

	out, err := r.run.Run(ctx, bridgeCmd(), "-j", "fdb", "show", "dev", dev)
	if err != nil {
		return fmt.Errorf("list peers of %s: %w", dev, err)
	}
	var entries []fdb
	if s := strings.TrimSpace(string(out)); s != "" {
		if err := json.Unmarshal([]byte(s), &entries); err != nil {
			return fmt.Errorf("list peers of %s: %w", dev, err)
		}
	}
	const flood = "00:00:00:00:00:00"
	current := map[netip.Addr]bool{}
	for _, e := range entries {
		if a, err := netip.ParseAddr(e.Dst); err == nil && e.MAC == flood {
			current[a] = true
		}
	}
	for _, p := range t.Peers {
		if !current[p] {
			if _, err := r.run.Run(ctx, bridgeCmd(), "fdb", "append", flood, "dev", dev, "dst", p.String()); err != nil {
				return fmt.Errorf("add peer %s: %w", p, err)
			}
		}
		delete(current, p)
	}
	for p := range current {
		if _, err := r.run.Run(ctx, bridgeCmd(), "fdb", "del", flood, "dev", dev, "dst", p.String()); err != nil {
			return fmt.Errorf("remove peer %s: %w", p, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]Tunnel) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// bridgeCmd finds iproute2's bridge, which lives in sbin (not on every PATH).
func bridgeCmd() string {
	if p, err := exec.LookPath("bridge"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/bridge", "/sbin/bridge"} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	return "bridge"
}

// Exec runs the commands on this host.
type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, errors.New(msg)
		}
	}
	return out, err
}
