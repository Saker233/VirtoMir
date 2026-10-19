package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onurbaydas/virtomir/internal/virt"
	"libvirt.org/go/libvirtxml"
)

const sampleDomainXML = `<domain type='kvm' id='42'>
  <name>ubuntu-template</name>
  <uuid>9a8b7c6d-5e4f-3a2b-1c0d-ef0123456789</uuid>
  <title>Ubuntu Golden Image</title>
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-8.2'>hvm</type>
    <loader readonly='yes' type='pflash'>/usr/share/OVMF/OVMF_CODE.fd</loader>
    <nvram template='/usr/share/OVMF/OVMF_VARS.fd'>/var/lib/libvirt/qemu/nvram/ubuntu-template_VARS.fd</nvram>
  </os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
      <address type='pci' domain='0x0000' bus='0x04' slot='0x00' function='0x0'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
      <address type='drive' controller='0' bus='0' target='0' unit='0'/>
    </disk>
    <controller type='usb' index='0' model='qemu-xhci'>
      <address type='pci' domain='0x0000' bus='0x02' slot='0x00' function='0x0'/>
    </controller>
    <interface type='network'>
      <mac address='52:54:00:11:22:33'/>
      <source network='default'/>
      <target dev='vnet0'/>
      <model type='virtio'/>
      <address type='pci' domain='0x0000' bus='0x01' slot='0x00' function='0x0'/>
    </interface>
    <graphics type='vnc' port='5901' autoport='no'/>
    <video>
      <model type='virtio'/>
      <address type='pci' domain='0x0000' bus='0x00' slot='0x01' function='0x0'/>
    </video>
  </devices>
</domain>`

func TestStreamFileSHA256(t *testing.T) {
	dir := t.TempDir()

	// 1. Empty file vector
	emptyPath := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantEmptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	hash, size, err := StreamFileSHA256(context.Background(), emptyPath)
	if err != nil {
		t.Fatalf("StreamFileSHA256 empty file: %v", err)
	}
	if size != 0 {
		t.Errorf("empty file size = %d, want 0", size)
	}
	if hash != wantEmptyHash {
		t.Errorf("empty file hash = %s, want %s", hash, wantEmptyHash)
	}

	// 2. Known payload vector
	payload := []byte("VirtoMir Content Library ADR-0063\n")
	h := sha256.Sum256(payload)
	wantPayloadHash := hex.EncodeToString(h[:])

	payloadPath := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, size, err = StreamFileSHA256(context.Background(), payloadPath)
	if err != nil {
		t.Fatalf("StreamFileSHA256 payload file: %v", err)
	}
	if size != int64(len(payload)) {
		t.Errorf("payload size = %d, want %d", size, len(payload))
	}
	if hash != wantPayloadHash {
		t.Errorf("payload hash = %s, want %s", hash, wantPayloadHash)
	}

	// 3. Context cancellation aborts streaming
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before read
	_, _, err = StreamFileSHA256(ctx, payloadPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSanitizeDomainXML(t *testing.T) {
	raw := `<domain type='kvm' id='5'>
  <name>srv-tpl</name>
  <uuid>7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f</uuid>
  <os>
    <type arch='x86_64'>hvm</type>
    <nvram template='/usr/share/OVMF/OVMF_VARS.fd'>/var/lib/libvirt/qemu/nvram/srv-tpl_VARS.fd</nvram>
  </os>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/var/lib/libvirt/images/srv.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <address type='pci' domain='0x0000' bus='0x04' slot='0x00' function='0x0'/>
    </disk>
    <interface type='network'>
      <mac address='52:54:00:AB:CD:EF'/>
      <source network='default'/>
      <target dev='vnet4'/>
      <address type='pci' domain='0x0000' bus='0x01' slot='0x00' function='0x0'/>
    </interface>
    <controller type='usb' index='0'>
      <address type='pci' domain='0x0000' bus='0x02' slot='0x00' function='0x0'/>
    </controller>
    <graphics type='vnc' port='5900' autoport='no'/>
    <graphics type='spice' port='5901' autoport='no'/>
  </devices>
</domain>`

	sanitized, err := SanitizeDomainXML(raw)
	if err != nil {
		t.Fatalf("SanitizeDomainXML failed: %v", err)
	}

	// String level assertions
	if strings.Contains(sanitized, "7c1f3e2a-55b4-4a8e-9d0c-1b2a3c4d5e6f") {
		t.Errorf("sanitized XML still contains UUID")
	}
	if strings.Contains(sanitized, "52:54:00:AB:CD:EF") {
		t.Errorf("sanitized XML still contains MAC address")
	}
	if strings.Contains(sanitized, "vnet4") {
		t.Errorf("sanitized XML still contains interface target dev")
	}
	if strings.Contains(sanitized, "/var/lib/libvirt/qemu/nvram/srv-tpl_VARS.fd") {
		t.Errorf("sanitized XML still contains NVRAM instance path")
	}
	if !strings.Contains(sanitized, "/usr/share/OVMF/OVMF_VARS.fd") {
		t.Errorf("sanitized XML lost NVRAM template reference")
	}
	if strings.Contains(sanitized, "address type='pci'") {
		t.Errorf("sanitized XML still contains fixed PCI addresses")
	}

	// Parsed structure assertions
	var parsed libvirtxml.Domain
	if err := parsed.Unmarshal(sanitized); err != nil {
		t.Fatalf("unmarshal sanitized XML: %v", err)
	}
	if parsed.UUID != "" {
		t.Errorf("parsed UUID not empty: %q", parsed.UUID)
	}
	if parsed.ID != nil {
		t.Errorf("parsed ID not nil: %v", parsed.ID)
	}
	if parsed.OS != nil && parsed.OS.NVRam != nil {
		if parsed.OS.NVRam.NVRam != "" {
			t.Errorf("parsed NVRam path not empty: %q", parsed.OS.NVRam.NVRam)
		}
		if parsed.OS.NVRam.Template != "/usr/share/OVMF/OVMF_VARS.fd" {
			t.Errorf("parsed NVRam template = %q", parsed.OS.NVRam.Template)
		}
	}
	if len(parsed.Devices.Interfaces) != 1 || parsed.Devices.Interfaces[0].MAC != nil {
		t.Errorf("interface MAC was not dropped: %+v", parsed.Devices.Interfaces[0].MAC)
	}
	if parsed.Devices.Disks[0].Address != nil {
		t.Errorf("disk address was not dropped: %+v", parsed.Devices.Disks[0].Address)
	}
	if parsed.Devices.Graphics[0].VNC.Port != -1 || parsed.Devices.Graphics[0].VNC.AutoPort != "yes" {
		t.Errorf("vnc port was not reset to auto: %+v", parsed.Devices.Graphics[0].VNC)
	}
	if parsed.Devices.Graphics[1].Spice.Port != -1 || parsed.Devices.Graphics[1].Spice.AutoPort != "yes" {
		t.Errorf("spice port was not reset to auto: %+v", parsed.Devices.Graphics[1].Spice)
	}
}

func TestPublishTemplate_NonTemplateRefused(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithDomainLookup(context.Background(), func(ctx context.Context, client *virt.Client, uuid string) (*domainInfo, error) {
		return &domainInfo{
			Name:     "regular-vm",
			Template: false, // NOT a template
			State:    virt.StateShutoff,
			XML:      "<domain type='kvm'><name>regular-vm</name></domain>",
		}, nil
	})

	_, err = PublishTemplate(ctx, nil, store, "host1", PublishTemplateReq{
		TemplateUUID: "regular-uuid",
		Name:         "regular-tpl",
		Version:      "1.0",
	})
	if err == nil {
		t.Fatal("expected error when publishing non-template VM, got nil")
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("expected ErrRefused, got %v", err)
	}
	if !strings.Contains(err.Error(), "not a template") {
		t.Errorf("expected error message to mention 'not a template', got %q", err.Error())
	}
}

func TestPublishTemplate_RunningRefused(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithDomainLookup(context.Background(), func(ctx context.Context, client *virt.Client, uuid string) (*domainInfo, error) {
		return &domainInfo{
			Name:     "running-vm",
			Template: true,
			State:    virt.StateRunning, // RUNNING
			XML:      "<domain type='kvm'><name>running-vm</name></domain>",
		}, nil
	})

	_, err = PublishTemplate(ctx, nil, store, "host1", PublishTemplateReq{
		TemplateUUID: "running-uuid",
		Name:         "running-tpl",
		Version:      "1.0",
	})
	if err == nil {
		t.Fatal("expected error when publishing running VM, got nil")
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("expected ErrRefused, got %v", err)
	}
	if !strings.Contains(err.Error(), "running") {
		t.Errorf("expected error message to mention 'running', got %q", err.Error())
	}
}

func TestPublishTemplate_SuccessAndVersioning(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	diskPath := filepath.Join(dir, "ubuntu.qcow2")
	diskData := append([]byte{'Q', 'F', 'I', 0xfb}, make([]byte, 1020)...) // 1024 bytes qcow2
	if err := os.WriteFile(diskPath, diskData, 0o600); err != nil {
		t.Fatal(err)
	}

	xmlDoc := strings.ReplaceAll(sampleDomainXML, "%s", diskPath)

	ctx := WithDomainLookup(context.Background(), func(ctx context.Context, client *virt.Client, uuid string) (*domainInfo, error) {
		return &domainInfo{
			Name:     "ubuntu-template",
			Template: true,
			State:    virt.StateShutoff,
			XML:      xmlDoc,
		}, nil
	})

	// 1. Initial version publishing
	item, err := PublishTemplate(ctx, nil, store, "node-1", PublishTemplateReq{
		TemplateUUID: "tpl-123",
		Name:         "Ubuntu-Server",
		Version:      "24.04-lts",
		Description:  "Base Ubuntu 24.04 server",
	})
	if err != nil {
		t.Fatalf("PublishTemplate failed: %v", err)
	}

	if item.Name != "Ubuntu-Server" {
		t.Errorf("item Name = %q, want Ubuntu-Server", item.Name)
	}
	if item.Type != TypeTemplate {
		t.Errorf("item Type = %q, want template", item.Type)
	}
	if item.SourceHost != "node-1" {
		t.Errorf("item SourceHost = %q, want node-1", item.SourceHost)
	}
	if len(item.Versions) != 1 {
		t.Fatalf("item versions count = %d, want 1", len(item.Versions))
	}
	v := item.Versions[0]
	if v.Version != "24.04-lts" {
		t.Errorf("version = %q", v.Version)
	}
	if v.Format != "qcow2" {
		t.Errorf("format = %q, want qcow2", v.Format)
	}
	if v.Size != 1024 {
		t.Errorf("size = %d, want 1024", v.Size)
	}
	if v.TemplateXML == "" {
		t.Fatal("template XML is empty")
	}
	if strings.Contains(v.TemplateXML, "52:54:00:11:22:33") {
		t.Errorf("published template XML contains original MAC address")
	}

	// 2. Publish second version under same name
	updated, err := PublishTemplate(ctx, nil, store, "node-1", PublishTemplateReq{
		TemplateUUID: "tpl-123",
		Name:         "Ubuntu-Server",
		Version:      "24.04.1-lts",
		Description:  "Ubuntu 24.04.1 point release",
	})
	if err != nil {
		t.Fatalf("PublishTemplate second version failed: %v", err)
	}
	if len(updated.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(updated.Versions))
	}
	if updated.Versions[1].Version != "24.04.1-lts" {
		t.Errorf("second version string = %q", updated.Versions[1].Version)
	}
}

func TestPublishVolume_MissingVolumeNotFound(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithVolumeList(context.Background(), func(ctx context.Context, client *virt.Client, pool string) ([]virt.Volume, error) {
		return []virt.Volume{
			{Name: "other-disk.raw", Path: "/images/other-disk.raw"},
		}, nil
	})

	_, err = PublishVolume(ctx, nil, store, "host1", PublishVolumeReq{
		Pool:    "default",
		Volume:  "missing.iso",
		Type:    TypeISO,
		Name:    "missing-iso",
		Version: "1.0",
	})
	if err == nil {
		t.Fatal("expected error for missing volume, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPublishVolume_InvalidType(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	_, err = PublishVolume(context.Background(), nil, store, "host1", PublishVolumeReq{
		Pool:    "default",
		Volume:  "vol.img",
		Type:    TypeTemplate, // INVALID for PublishVolume
		Name:    "vol",
		Version: "1.0",
	})
	if err == nil {
		t.Fatal("expected error for invalid volume type, got nil")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestPublishVolume_Success(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	// 1. Publish QCOW2 cloud image
	qcow2Path := filepath.Join(dir, "debian-cloud.qcow2")
	qcow2Data := append([]byte{'Q', 'F', 'I', 0xfb}, make([]byte, 2044)...) // 2048 bytes
	if err := os.WriteFile(qcow2Path, qcow2Data, 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Publish ISO image
	isoPath := filepath.Join(dir, "debian-netinst.iso")
	isoData := make([]byte, 1024)
	copy(isoData, []byte("CD001")) // not qcow2
	if err := os.WriteFile(isoPath, isoData, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := WithVolumeList(context.Background(), func(ctx context.Context, client *virt.Client, pool string) ([]virt.Volume, error) {
		return []virt.Volume{
			{Name: "debian-cloud.qcow2", Path: qcow2Path, Format: "qcow2"},
			{Name: "debian-netinst.iso", Path: isoPath, Format: "raw"},
		}, nil
	})

	// Publish cloud image
	itemImg, err := PublishVolume(ctx, nil, store, "host-a", PublishVolumeReq{
		Pool:        "images",
		Volume:      "debian-cloud.qcow2",
		Type:        TypeImage,
		Name:        "Debian-Cloud",
		Version:     "12.0",
		Description: "Official Debian cloud image",
	})
	if err != nil {
		t.Fatalf("PublishVolume image failed: %v", err)
	}
	if itemImg.Name != "Debian-Cloud" || itemImg.Type != TypeImage {
		t.Fatalf("image item metadata mismatch: %+v", itemImg)
	}
	if len(itemImg.Versions) != 1 {
		t.Fatalf("image versions = %d, want 1", len(itemImg.Versions))
	}
	vImg := itemImg.Versions[0]
	if vImg.Format != "qcow2" {
		t.Errorf("image format = %q, want qcow2", vImg.Format)
	}
	if vImg.Size != 2048 {
		t.Errorf("image size = %d, want 2048", vImg.Size)
	}
	if vImg.SourcePool != "images" || vImg.SourceVol != "debian-cloud.qcow2" || vImg.SourceHost != "host-a" {
		t.Errorf("image source info mismatch: %+v", vImg)
	}

	// Publish ISO
	itemISO, err := PublishVolume(ctx, nil, store, "host-a", PublishVolumeReq{
		Pool:        "images",
		Volume:      "debian-netinst.iso",
		Type:        TypeISO,
		Name:        "Debian-ISO",
		Version:     "12.7",
		Description: "Debian 12.7 installer",
	})
	if err != nil {
		t.Fatalf("PublishVolume ISO failed: %v", err)
	}
	if itemISO.Type != TypeISO {
		t.Fatalf("expected iso type, got %s", itemISO.Type)
	}
	vISO := itemISO.Versions[0]
	if vISO.Format != "iso" {
		t.Errorf("iso format = %q, want iso", vISO.Format)
	}
	if vISO.Size != 1024 {
		t.Errorf("iso size = %d, want 1024", vISO.Size)
	}
}

func TestPublishArtifact(t *testing.T) {
	store, err := Open("")
	if err != nil {
		t.Fatal(err)
	}

	dummyHash := "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	v1 := ItemVersion{
		Version: "1.0",
		SHA256:  dummyHash,
		Size:    5000,
		Format:  "raw",
	}

	// 1. Initial creation via PublishArtifact
	item, err := PublishArtifact(store, Item{
		Name: "custom-tooling",
		Type: TypeImage,
	}, v1)
	if err != nil {
		t.Fatalf("PublishArtifact create failed: %v", err)
	}
	if item.Name != "custom-tooling" || len(item.Versions) != 1 {
		t.Fatalf("PublishArtifact item mismatch: %+v", item)
	}

	// 2. Append new version via PublishArtifact
	v2 := ItemVersion{
		Version: "2.0",
		SHA256:  dummyHash,
		Size:    6000,
		Format:  "raw",
	}
	updated, err := PublishArtifact(store, Item{Name: "custom-tooling"}, v2)
	if err != nil {
		t.Fatalf("PublishArtifact add version failed: %v", err)
	}
	if len(updated.Versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(updated.Versions))
	}
}
