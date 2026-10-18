package virt

import (
	"slices"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestConfidentialFromDomainCapabilities(t *testing.T) {
	for _, c := range []struct {
		xml  string
		want []string
	}{
		{`<domainCapabilities><features><sev supported='no'/><launchSecurity supported='no'/></features></domainCapabilities>`, nil},
		{`<domainCapabilities><features><sev supported='yes'><cbitpos>51</cbitpos><reducedPhysBits>1</reducedPhysBits></sev>
			<launchSecurity supported='yes'><enum name='sectype'><value>sev</value><value>sev-snp</value></enum></launchSecurity></features></domainCapabilities>`, []string{"sev", "sev-snp"}},
		{`<domainCapabilities><features><tdx supported='yes'/></features></domainCapabilities>`, []string{"tdx"}},
	} {
		var caps libvirtxml.DomainCaps
		if err := caps.Unmarshal(c.xml); err != nil {
			t.Fatal(err)
		}
		got := parseConfidential(&caps)
		if !slices.Equal(got.kinds, c.want) {
			t.Errorf("kinds = %v, want %v", got.kinds, c.want)
		}
		if len(c.want) == 2 && (got.sev == nil || got.sev.CBitPos != 51) {
			t.Errorf("sev = %+v", got.sev)
		}
	}
	bad := CreateSpec{Name: "x", VCPUs: 1, MemoryMiB: 512, Firmware: "bios", Confidential: "sev", ISO: "/i.iso"}
	if bad.normalize() == nil {
		t.Error("SEV with BIOS accepted")
	}
	bad.Firmware, bad.Confidential = "uefi", "sgx"
	if bad.normalize() == nil {
		t.Error("unknown memory encryption accepted")
	}
}
