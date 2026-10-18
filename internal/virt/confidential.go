package virt

import (
	"slices"

	"github.com/digitalocean/go-libvirt"
	"libvirt.org/go/libvirtxml"
)

// Confidential machines (memory encryption: AMD SEV / SEV-SNP, Intel TDX) need CPU,
// firmware and kernel support; libvirt's domain capabilities say what this host offers.

type confidentialCaps struct {
	kinds []string // sev, sev-snp, tdx
	sev   *libvirtxml.DomainCapsFeatureSEV
}

func (c *Client) confidentialCaps(l *libvirt.Libvirt) (confidentialCaps, error) {
	raw, err := l.ConnectGetDomainCapabilities(libvirt.OptString{}, libvirt.OptString{}, libvirt.OptString{"q35"}, libvirt.OptString{"kvm"}, 0)
	if err != nil {
		return confidentialCaps{}, classify(err)
	}
	var caps libvirtxml.DomainCaps
	if err := caps.Unmarshal(raw); err != nil {
		return confidentialCaps{}, err
	}
	return parseConfidential(&caps), nil
}

func parseConfidential(caps *libvirtxml.DomainCaps) confidentialCaps {
	var out confidentialCaps
	f := caps.Features
	if f == nil {
		return out
	}
	if ls := f.LaunchSecurity; ls != nil && ls.Supported == "yes" {
		for _, e := range ls.Enums {
			if e.Name == "sectype" {
				for _, v := range e.Values {
					if v == "sev" || v == "sev-snp" || v == "tdx" {
						out.kinds = append(out.kinds, v)
					}
				}
			}
		}
	}
	// Older libvirt has no launchSecurity element, only sev / tdx.
	if f.SEV != nil && f.SEV.Supported == "yes" {
		out.sev = f.SEV
		if !slices.Contains(out.kinds, "sev") {
			out.kinds = append(out.kinds, "sev")
		}
	}
	if f.TDX != nil && f.TDX.Supported == "yes" && !slices.Contains(out.kinds, "tdx") {
		out.kinds = append(out.kinds, "tdx")
	}
	slices.Sort(out.kinds)
	return out
}

// ConfidentialKinds is what memory encryption this host can give new machines.
func (c *Client) ConfidentialKinds() ([]string, error) {
	l, err := c.conn()
	if err != nil {
		return nil, err
	}
	caps, err := c.confidentialCaps(l)
	if err != nil {
		return nil, err
	}
	return append([]string{}, caps.kinds...), nil
}

// applyConfidential checks that the host offers s.Confidential and fills in the SEV
// parameters libvirt needs from the host's own values.
func (c *Client) applyConfidential(l *libvirt.Libvirt, s CreateSpec, x *libvirtxml.Domain) error {
	if s.Confidential == "" {
		return nil
	}
	caps, err := c.confidentialCaps(l)
	if err != nil {
		return err
	}
	if !slices.Contains(caps.kinds, s.Confidential) {
		return refusedf("This host cannot run %s machines: its CPU, firmware or kernel does not offer it (libvirt's domain capabilities).", s.Confidential)
	}
	if sev := x.LaunchSecurity; sev != nil && sev.SEV != nil && caps.sev != nil {
		cbit, reduced := caps.sev.CBitPos, caps.sev.ReducedPhysBits
		sev.SEV.CBitPos, sev.SEV.ReducedPhysBits = &cbit, &reduced
	}
	return nil
}
