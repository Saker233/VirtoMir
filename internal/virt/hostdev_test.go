package virt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPCIBlocked(t *testing.T) {
	g := 7
	gpu := HostPCI{Address: "0000:01:00.0", Class: "0x030000", IOMMUGroup: &g, Group: []string{"0000:01:00.1", "0000:00:01.0"}}
	drivers := map[string]string{"0000:01:00.1": "snd_hda_intel", "0000:00:01.0": "pcieport"}
	bridge := func(a string) bool { return a == "0000:00:01.0" }

	if r := pciBlocked(gpu, false, drivers, bridge); !strings.Contains(r, "IOMMU is off") {
		t.Errorf("no IOMMU: %q", r)
	}
	// The GPU's audio function is still on a host driver: the group is not free.
	if r := pciBlocked(gpu, true, drivers, bridge); !strings.Contains(r, "0000:01:00.1 (snd_hda_intel)") || strings.Contains(r, "pcieport") {
		t.Errorf("busy group: %q", r)
	}
	drivers["0000:01:00.1"] = "vfio-pci"
	if r := pciBlocked(gpu, true, drivers, bridge); r != "" {
		t.Errorf("free group refused: %q", r)
	}
	nic := gpu
	nic.HostUse = "network interface eno1 has an address (192.168.1.2/24)"
	if r := pciBlocked(nic, true, drivers, bridge); !strings.Contains(r, "host needs it") {
		t.Errorf("host NIC: %q", r)
	}
	br := HostPCI{Class: "0x060400", IOMMUGroup: &g}
	if r := pciBlocked(br, true, drivers, bridge); !strings.Contains(r, "bridge") {
		t.Errorf("bridge: %q", r)
	}
	if r := pciBlocked(HostPCI{Class: "0x020000"}, true, drivers, bridge); !strings.Contains(r, "not in an IOMMU group") {
		t.Errorf("no group: %q", r)
	}
}

func TestParsePCIAddress(t *testing.T) {
	a, err := parsePCIAddress("0000:0a:1f.3")
	if err != nil || pciAddr(a) != "0000:0a:1f.3" {
		t.Fatalf("= %v, %v", a, err)
	}
	for _, bad := range []string{"", "0a:1f.3", "zzzz:00:00.0"} {
		if _, err := parsePCIAddress(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReadSRIOV(t *testing.T) {
	tmp := t.TempDir()
	orig := sysPciDevices
	sysPciDevices = tmp
	t.Cleanup(func() { sysPciDevices = orig })

	// Case 1: Physical Function with 16 total VFs and 4 active VFs
	pfAddr := "0000_03_00.0"
	pfDir := filepath.Join(tmp, pfAddr)
	if err := os.MkdirAll(pfDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(pfDir, "sriov_totalvfs"), []byte("16\n"), 0644)
	_ = os.WriteFile(filepath.Join(pfDir, "sriov_numvfs"), []byte("4\n"), 0644)

	isPF, total, num, isVF, parent := readSRIOV(pfAddr)
	if !isPF || total != 16 || num != 4 || isVF || parent != "" {
		t.Errorf("unexpected PF result: isPF=%v total=%d num=%d isVF=%v parent=%s", isPF, total, num, isVF, parent)
	}

	// Case 2: Virtual Function pointing to the PF
	vfAddr := "0000_03_10.0"
	vfDir := filepath.Join(tmp, vfAddr)
	if err := os.MkdirAll(vfDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(pfDir, filepath.Join(vfDir, "physfn"))

	isPF2, _, _, isVF2, parent2 := readSRIOV(vfAddr)
	if isVF2 && parent2 != pfAddr {
		t.Errorf("unexpected VF parent: %s", parent2)
	}
	if isPF2 {
		t.Errorf("VF marked as PF")
	}
}
