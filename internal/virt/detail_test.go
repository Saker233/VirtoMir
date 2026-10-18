package virt

import (
	"os"
	"slices"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func loadDetail(t *testing.T, file string) DomainDetail {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var x libvirtxml.Domain
	if err := x.Unmarshal(string(raw)); err != nil {
		t.Fatal(err)
	}
	return buildDetail(Domain{Name: x.Name, State: StateRunning}, &x)
}

func findDevice(t *testing.T, d DomainDetail, title string) Device {
	t.Helper()
	for _, dev := range d.Devices {
		if dev.Title == title {
			return dev
		}
	}
	var titles []string
	for _, dev := range d.Devices {
		titles = append(titles, dev.Title)
	}
	t.Fatalf("no device %q in %q", title, titles)
	return Device{}
}

func prop(dev Device, label string) string {
	for _, p := range dev.Props {
		if p.Label == label {
			return p.Value
		}
	}
	return ""
}

func TestBuildDetailSystem(t *testing.T) {
	d := loadDetail(t, "testdata/debian-uefi.xml")
	s := d.System
	if d.Title != "Debian test box" || s.Hypervisor != "kvm" || s.Arch != "x86_64" || s.Machine != "pc-q35-10.0" {
		t.Errorf("identity: title=%q system=%+v", d.Title, s)
	}
	if s.Firmware != "uefi" || !s.SecureBoot || !s.BootMenu {
		t.Errorf("firmware=%q secureBoot=%v bootMenu=%v", s.Firmware, s.SecureBoot, s.BootMenu)
	}
	if s.NVRAM != "/var/lib/libvirt/qemu/nvram/debian-uefi_VARS.fd" {
		t.Errorf("nvram = %q", s.NVRAM)
	}
	// vmport is off, so it must not be listed.
	if want := []string{"acpi", "apic", "smm"}; !slices.Equal(s.Features, want) {
		t.Errorf("features = %q, want %q", s.Features, want)
	}
	// Per-device boot order wins over <os><boot dev='hd'/>.
	if want := []string{"CD/DVD drive sda", "Disk vda"}; !slices.Equal(s.BootOrder, want) {
		t.Errorf("boot order = %q, want %q", s.BootOrder, want)
	}
}

func TestBuildDetailCPUAndMemory(t *testing.T) {
	d := loadDetail(t, "testdata/debian-uefi.xml")
	c := d.CPU
	if c.Mode != "host-passthrough" || c.Sockets != 1 || c.Cores != 2 || c.Threads != 2 || c.VCPUs != 2 || c.MaxVCPUs != 4 || !c.Pinned {
		t.Errorf("cpu = %+v", c)
	}
	m := d.Memory
	if m.MaxKiB != 4194304 || m.CurrentKiB != 2097152 || m.Balloon != "virtio" || m.Hugepages {
		t.Errorf("memory = %+v", m)
	}
}

func TestBuildDetailDevices(t *testing.T) {
	d := loadDetail(t, "testdata/debian-uefi.xml")

	disk := findDevice(t, d, "Disk vda")
	if disk.Group != GroupStorage || disk.Summary != "VirtIO · QCOW2 · debian-uefi.qcow2" || disk.Boot != 2 || disk.Target != "vda" {
		t.Errorf("disk = %+v", disk)
	}
	if prop(disk, "Backing image") != "/var/lib/libvirt/images/debian-13-base.qcow2" || prop(disk, "Cache mode") != "none" {
		t.Errorf("disk props = %+v", disk.Props)
	}
	if prop(disk, "Guest address") != "PCI 0000:04:00.0" {
		t.Errorf("guest address = %q", prop(disk, "Guest address"))
	}

	cd := findDevice(t, d, "CD/DVD drive sda")
	if cd.Kind != "cdrom" || cd.Summary != "SATA · RAW · Empty" || prop(cd, "Read-only") != "yes" {
		t.Errorf("cdrom = %+v", cd)
	}

	nic := findDevice(t, d, "Network adapter")
	if nic.MAC != "52:54:00:ab:cd:ef" || nic.Summary != "Virtual network default · VirtIO" || prop(nic, "Host interface") != "vnet2" {
		t.Errorf("nic = %+v", nic)
	}

	vnc := findDevice(t, d, "Display (VNC)")
	if prop(vnc, "Port") != "5900" || prop(vnc, "Listen address") != "127.0.0.1" {
		t.Errorf("vnc = %+v", vnc)
	}

	ga := findDevice(t, d, "QEMU guest agent")
	if ga.Group != GroupConsole || ga.Summary != "Connected" {
		t.Errorf("guest agent = %+v", ga)
	}

	if s := findDevice(t, d, "Serial port 0"); s.Summary != "isa-serial · pseudo-terminal /dev/pts/3" {
		t.Errorf("serial summary = %q", s.Summary)
	}
	if tpm := findDevice(t, d, "TPM 2.0"); tpm.Summary != "tpm-crb · emulated (swtpm)" {
		t.Errorf("tpm summary = %q", tpm.Summary)
	}
	if usb := findDevice(t, d, "USB device"); usb.Group != GroupPassthrough || usb.Summary != "046d:c52b" {
		t.Errorf("usb hostdev = %+v", usb)
	}
	if ctl := findDevice(t, d, "USB controller 0"); ctl.Group != GroupController || ctl.Summary != "qemu-xhci" {
		t.Errorf("controller = %+v", ctl)
	}
	findDevice(t, d, "Memory balloon")
	findDevice(t, d, "Random number generator")
	findDevice(t, d, "Tablet (absolute pointer)")
}

func TestBuildDetailMinimal(t *testing.T) {
	// A domain with almost nothing set must still produce a usable model.
	var x libvirtxml.Domain
	if err := x.Unmarshal(`<domain type='qemu'><name>bare</name><memory>1048576</memory><vcpu>2</vcpu><os><type>hvm</type><boot dev='network'/></os></domain>`); err != nil {
		t.Fatal(err)
	}
	d := buildDetail(Domain{Name: "bare"}, &x)
	if d.System.Firmware != "bios" || d.CPU.Sockets != 2 || d.CPU.Cores != 1 || d.Memory.CurrentKiB != 1048576 {
		t.Errorf("detail = %+v", d)
	}
	if d.Devices == nil || len(d.Devices) != 0 {
		t.Errorf("devices must be an empty list, got %#v", d.Devices)
	}
	if want := []string{"Network (PXE)"}; !slices.Equal(d.System.BootOrder, want) {
		t.Errorf("boot order = %q", d.System.BootOrder)
	}
}

func TestToKiB(t *testing.T) {
	cases := []struct {
		v    uint
		unit string
		want uint64
	}{
		{1024, "KiB", 1024},
		{4, "GiB", 4 << 20},
		{512, "MiB", 512 << 10},
		{2048, "", 2048},
		{1048576, "bytes", 1024},
	}
	for _, c := range cases {
		if got := toKiB(c.v, c.unit); got != c.want {
			t.Errorf("toKiB(%d, %q) = %d, want %d", c.v, c.unit, got, c.want)
		}
	}
}

func TestDiskIoTuneParsing(t *testing.T) {
	xml := `<domain type='kvm'>
  <name>iotune-vm</name>
  <uuid>00000000-0000-0000-0000-000000000001</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>1</vcpu>
  <os><type>hvm</type></os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/tmp/test.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <iotune>
        <total_bytes_sec>104857600</total_bytes_sec>
        <total_iops_sec>2500</total_iops_sec>
        <read_iops_sec>1000</read_iops_sec>
        <write_iops_sec>1500</write_iops_sec>
      </iotune>
    </disk>
  </devices>
</domain>`
	var x libvirtxml.Domain
	if err := x.Unmarshal(xml); err != nil {
		t.Fatal(err)
	}
	d := buildDetail(Domain{Name: x.Name, State: StateRunning}, &x)
	dev := findDevice(t, d, "Disk vda")
	if dev.IoTune == nil {
		t.Fatal("expected dev.IoTune to be non-nil")
	}
	if dev.IoTune.TotalBytesSec != 104857600 || dev.IoTune.TotalIopsSec != 2500 || dev.IoTune.ReadIopsSec != 1000 || dev.IoTune.WriteIopsSec != 1500 {
		t.Errorf("unexpected IoTune: %+v", dev.IoTune)
	}
	if p := prop(dev, "I/O limit (ceiling)"); p != "2500 IOPS" {
		t.Errorf("expected ceiling prop 2500 IOPS, got %q", p)
	}
}

func TestConfidentialComputing(t *testing.T) {
	xml := `<domain type='kvm'>
  <name>sev-vm</name>
  <uuid>00000000-0000-0000-0000-000000000002</uuid>
  <memory unit='KiB'>1048576</memory>
  <vcpu>2</vcpu>
  <os><type>hvm</type></os>
  <launchSecurity type='sev'>
    <cbitpos>47</cbitpos>
    <reducedPhysBits>1</reducedPhysBits>
    <policy>0x0003</policy>
  </launchSecurity>
</domain>`
	var x libvirtxml.Domain
	if err := x.Unmarshal(xml); err != nil {
		t.Fatal(err)
	}
	d := buildDetail(Domain{Name: x.Name, State: StateRunning}, &x)
	if d.System.LaunchSecurity != "sev" {
		t.Errorf("expected LaunchSecurity sev, got %q", d.System.LaunchSecurity)
	}
	if !slices.Contains(d.System.Features, "confidential (sev)") {
		t.Errorf("expected feature 'confidential (sev)', got %v", d.System.Features)
	}
}

func mustRead(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestContainerDomain(t *testing.T) {
	spec := CreateSpec{
		Name:         "c-alpine",
		Kind:         "container",
		VCPUs:        2,
		MemoryMiB:    512,
		RootPassword: "correct horse",
		Network:      &NICSpec{Network: "default"},
	}
	if err := spec.normalize(); err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	for _, pw := range []string{"short", "has:colon", "line\nbreak"} {
		bad := spec
		bad.RootPassword = pw
		if bad.normalize() == nil {
			t.Errorf("root password %q accepted", pw)
		}
	}
	xml, err := containerXML(spec, "/var/lib/virtomir/containers/c-alpine/rootfs").Marshal()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, want := range []string{`type="lxc"`, "<type>exe</type>", "<init>/sbin/init</init>",
		`<uid start="0" target="100000" count="65536">`, `<source dir="/var/lib/virtomir/containers/c-alpine/rootfs">`,
		`<target type="lxc"`, `<source network="default">`} {
		if !strings.Contains(xml, want) {
			t.Errorf("container XML lacks %s:\n%s", want, xml)
		}
	}
	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(xml); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	d := buildDetail(Domain{Name: "c-alpine"}, &parsed)
	if d.Domain.Kind != "container" {
		t.Errorf("expected Kind container, got %q", d.Domain.Kind)
	}
}
