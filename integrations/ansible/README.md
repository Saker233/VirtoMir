# onurbaydas.virtomir

Ansible collection for [VirtoMir](https://github.com/onurbaydas/VirtoMir) — "A Detailed Control Deck for Virtual Environments."

This collection provides complete, idempotent, and check-mode capable modules for orchestrating KVM/QEMU/libvirt virtualization infrastructure, virtual networking, storage pools, multi-tenant projects (ADR-0060), placement constraints (ADR-0062), and VM snapshots.

---

## Installation

```bash
# Build and install locally
ansible-galaxy collection build
ansible-galaxy collection install onurbaydas-virtomir-*.tar.gz
```

---

## Connection & Common Arguments

All modules accept connection options directly or inherit them automatically via `module_defaults` or environment variables:

| Parameter | Type | Default | Environment Fallback | Description |
|-----------|------|---------|----------------------|-------------|
| `url` | `str` | Required | `VIRTOMIR_URL` | Base URL of the VirtoMir daemon (e.g. `https://hv1:8420`). |
| `token` | `str` | Optional | `VIRTOMIR_TOKEN` | Bearer API token with required role (`admin`, `operator`, or `viewer`). |
| `validate_certs` | `bool` | `true` | - | Whether to verify the daemon's TLS certificate. |
| `host` | `str` | Optional | - | Target cluster member ID or hostname when accessing through cluster federation proxy. |

### Module Defaults Example

```yaml
- hosts: localhost
  collections: [onurbaydas.virtomir]
  module_defaults:
    group/onurbaydas.virtomir.all:
      url: https://hv1.internal:8420
      token: "{{ lookup('env', 'VIRTOMIR_TOKEN') }}"
      validate_certs: false
```

---

## Modules

### 1. `virtomir_machine` (alias `machine`)

Create from template (instant linked clone or full copy) or scratch (new disk, cloud image, ISO, existing disk), resize vCPU/RAM, control power, set autostart, and remove.

#### Parameters

| Parameter | Type | Default | Choices / Format | Description |
|-----------|------|---------|------------------|-------------|
| `name` | `str` | **Required** | - | Virtual machine name or UUID. |
| `state` | `str` | `present` | `present`, `running`, `stopped`, `absent` | Desired execution or existence state. |
| `template` | `str` | - | - | Name or UUID of template to deploy from. |
| `linked` | `bool` | `false` | - | Create an instant linked clone when deploying from template. |
| `os` | `str` | `linux` | `linux`, `windows`, `other` | OS family preset. |
| `vcpus` | `int` | - | - | Virtual CPU count (resized dynamically if machine exists). |
| `memory_mb` | `int` | - | - | Memory in MiB (resized dynamically if machine exists). |
| `firmware` | `str` | - | `uefi`, `bios` | Firmware mode for new machine creation. |
| `pool` | `str` | - | - | Storage pool for new disk or base image copy. |
| `disk_gb` | `int` | - | - | Disk size in GiB. |
| `base_image` | `str` | - | - | Path of cloud image to clone. |
| `disk_path` | `str` | - | - | Existing disk image path to boot. |
| `iso` | `str` | - | - | Installation ISO image path. |
| `network` | `str` | - | - | VirtoMir virtual network name. |
| `bridge` | `str` | - | - | Host bridge interface (e.g. `br0`). |
| `cloud_init` | `dict` | - | `{user, password, ssh_keys, hostname, packages}` | First-boot cloud-init provisioning. |
| `autostart` | `bool` | - | - | Start virtual machine on host boot. |
| `delete_disks`| `bool` | `true` | - | Remove storage volumes when `state=absent`. |
| `force` | `bool` | `false` | - | Force power-off instead of ACPI graceful shutdown. |

#### Example

```yaml
- name: Deploy linked-clone web server
  onurbaydas.virtomir.machine:
    name: web-01
    template: debian-13-cloud
    linked: true
    vcpus: 4
    memory_mb: 4096
    cloud_init:
      user: ops
      ssh_keys: ["{{ lookup('file', '~/.ssh/id_ed25519.pub') }}"]
      packages: [nginx, curl]
    state: running
```

---

### 2. `virtomir_network` (alias `network`)

Manage virtual networks including NAT, routed, isolated, and bridge modes with IPv4 and IPv6 dual-stack support. Fully idempotent with check mode and diff reporting.

#### Parameters

| Parameter | Type | Default | Choices / Format | Description |
|-----------|------|---------|------------------|-------------|
| `name` | `str` | **Required** | - | Unique network name. |
| `mode` | `str` | `nat` | `nat`, `route`, `isolated`, `bridge` | Forwarding topology mode. |
| `bridge` | `str` | - | - | Host bridge interface name (required when `mode=bridge`). |
| `dhcp` | `bool` | `true` | - | Enable IPv4 DHCP service for `ipv4_subnet`. Set to `false` to disable DHCP. |
| `ipv4_subnet` | `str` | - | CIDR (e.g. `192.168.100.0/24`) | Subnet for NAT/route/isolated. |
| `ipv4_dhcp_start` | `str` | - | IP address | First IP of the DHCP range. |
| `ipv4_dhcp_end` | `str` | - | IP address | Last IP of the DHCP range. |
| `ipv6_subnet` | `str` | - | CIDR (e.g. `fd12:3456:789a::/64`)| /64 IPv6 prefix for SLAAC (and DHCPv6 if enabled). |
| `dhcp6` | `bool` | `false` | - | Enable IPv6 DHCPv6 service for `ipv6_subnet`. |
| `ipv6_dhcp_start` | `str` | - | IPv6 address | First address of IPv6 DHCP range. |
| `ipv6_dhcp_end` | `str` | - | IPv6 address | Last address of IPv6 DHCP range. |
| `autostart` | `bool` | `true` | - | Autostart network on host boot. |
| `state` | `str` | `present` | `present`, `absent`, `active`, `inactive` | Desired lifecycle state. |

#### Example

```yaml
- name: Create isolated database network
  onurbaydas.virtomir.network:
    name: db-isolated
    mode: isolated
    ipv4_subnet: 10.10.50.0/24
    autostart: true
    state: active

- name: Bind to physical LAN bridge
  onurbaydas.virtomir.network:
    name: lan-bridge
    mode: bridge
    bridge: br0
    autostart: true
    state: present
```

---

### 3. `virtomir_pool` (alias `pool`)

Manage storage pools (local directory, LVM volume groups, ZFS zpools, NFS shares, iSCSI portals). Supports check mode, autostart management, and activation control.

#### Parameters

| Parameter | Type | Default | Choices / Format | Description |
|-----------|------|---------|------------------|-------------|
| `name` | `str` | **Required** | - | Storage pool name. |
| `type` | `str` | `dir` | `dir`, `logical`, `zfs`, `netfs`, `iscsi` | Storage pool driver type. |
| `path` | `str` | - | Absolute path | Directory path on host (or mountpoint for netfs). |
| `source` | `str` | - | - | LVM volume group name or ZFS zpool name. |
| `host` | `str` | - | Hostname or IP | NFS server or iSCSI portal address. |
| `export` | `str` | - | Directory path | Remote NFS export path. |
| `target` | `str` | - | IQN | Target IQN for iSCSI. |
| `devices` | `list[str]` | - | Block device paths | List of disks to build a new LVM volume group. |
| `autostart` | `bool` | `true` | - | Automatically activate pool on host boot. |
| `state` | `str` | `present` | `present`, `absent`, `active`, `inactive` | Desired lifecycle state. |

#### Example

```yaml
- name: Configure fast NVMe image pool
  onurbaydas.virtomir.pool:
    name: nvme-images
    type: dir
    path: /mnt/fast-storage/virtomir
    autostart: true
    state: active

- name: Attach shared NFS pool
  onurbaydas.virtomir.pool:
    name: cluster-nfs
    type: netfs
    path: /mnt/cluster-nfs
    host: storage.san.local
    export: /exports/vms
    autostart: true
    state: present
```

---

### 4. `virtomir_project` (alias `project`)

Manage multi-tenant isolation boundaries, member role assignments, resource quotas, and permitted storage pools and networks (ADR-0060). Only applies updates via `PUT /api/v1/projects/{id}` when configuration differences exist.

#### Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `id` | `str` | **Required** | Project slug ID (`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`). |
| `name` | `str` | **Required** | Display name for the project. |
| `description` | `str` | `""` | Optional project summary. |
| `members` | `list[dict]` | `[]` | List of member mappings: `kind` (`user`\|`group`), `name` (`str`), `role` (`viewer`\|`operator`\|`admin`). |
| `limits` | `dict` | `{}` | Quota limits: `machines` (`int`), `vcpus` (`int`), `memory_mib` (`int`), `disk_gib` (`int`). `0` = unlimited. |
| `storage_pools` | `list[str]` | `[]` | Whitelist of allowed storage pool names for this project. |
| `networks` | `list[str]` | `[]` | Whitelist of allowed virtual networks for this project. |
| `state` | `str` | `present` | `present` or `absent`. |

#### Example

```yaml
- name: Provision tenant project with quotas
  onurbaydas.virtomir.project:
    id: finance-core
    name: "Finance Production"
    description: "Tenant boundary for accounting and billing systems"
    members:
      - kind: group
        name: finance-admins
        role: admin
      - kind: user
        name: fin-operator
        role: operator
    limits:
      machines: 20
      vcpus: 80
      memory_mib: 131072
      disk_gib: 2000
    storage_pools: [nvme-images, cluster-nfs]
    networks: [db-isolated, lan-bridge]
    state: present
```

---

### 5. `virtomir_placement_rule` (alias `placement_rule`)

Configure placement constraints (ADR-0062) for VM anti-affinity, VM affinity, or VM-to-host binding across cluster members. Automatically resolves VM names to UUIDs via the VirtoMir API.

#### Parameters

| Parameter | Type | Default | Choices / Description |
|-----------|------|---------|-----------------------|
| `name` | `str` | **Required** | Unique placement rule name. |
| `kind` | `str` | **Required** | `vm_affinity` (keep VMs together), `vm_anti_affinity` (spread across hosts), or `vm_host` (bind to hosts). |
| `strength` | `str` | `must` | `must` (hard constraint) or `should` (soft preference). |
| `domains` | `list[str]` | `[]` | List of VM names or UUIDs participating in the rule. |
| `hosts` | `list[str]` | `[]` | Target cluster member IDs for `kind=vm_host`. |
| `exclude` | `bool` | `false` | When true with `kind=vm_host`, domains must/should NOT run on named hosts. |
| `enabled` | `bool` | `true` | Whether active enforcement by scheduler, DRS, and HA is enabled. |
| `state` | `str` | `present` | `present` or `absent`. |

#### Example

```yaml
- name: Ensure database HA pair runs on separate physical hosts
  onurbaydas.virtomir.placement_rule:
    name: pgsql-cluster-anti-affinity
    kind: vm_anti_affinity
    strength: must
    domains: [pgsql-node-1, pgsql-node-2]
    enabled: true
    state: present

- name: Keep batch worker VMs off primary hypervisor
  onurbaydas.virtomir.placement_rule:
    name: avoid-primary-host
    kind: vm_host
    strength: should
    domains: [batch-worker-1, batch-worker-2]
    hosts: [hv-01]
    exclude: true
    state: present
```

---

### 6. `virtomir_snapshot` (alias `snapshot`)

Manage virtual machine snapshots with instant tree navigation, live memory state capture, and rollback restoration.

#### Parameters

| Parameter | Type | Default | Choices / Description |
|-----------|------|---------|-----------------------|
| `machine` | `str` | **Required** | Target machine name or UUID. |
| `name` | `str` | **Required** | Snapshot identifier name. |
| `description` | `str` | `""` | Human-readable annotation. |
| `memory` | `bool` | `false` | Capture RAM state alongside disks for instant resume. |
| `state` | `str` | `present` | `present` (create if absent, no-op if exists), `reverted` (restore to snapshot, no-op if already current), or `absent`. |

#### Example

```yaml
- name: Create snapshot before maintenance
  onurbaydas.virtomir.snapshot:
    machine: web-01
    name: pre-patch-snap
    description: "Before system kernel update"
    memory: false
    state: present

- name: Restore to snapshot if verification fails
  onurbaydas.virtomir.snapshot:
    machine: web-01
    name: pre-patch-snap
    state: reverted

- name: Clean up snapshot after validation
  onurbaydas.virtomir.snapshot:
    machine: web-01
    name: pre-patch-snap
    state: absent
```

---

## Complete End-to-End Playbook Example

```yaml
---
- name: End-to-end VirtoMir Infrastructure Provisioning
  hosts: localhost
  collections:
    - onurbaydas.virtomir
  module_defaults:
    group/onurbaydas.virtomir.all:
      url: https://virtomir.local:8420
      token: "{{ lookup('env', 'VIRTOMIR_TOKEN') }}"
      validate_certs: false

  tasks:
    - name: 1. Provision virtual network
      virtomir_network:
        name: prod-vpc
        mode: nat
        ipv4_subnet: 10.20.0.0/24
        autostart: true
        state: active

    - name: 2. Provision storage pool
      virtomir_pool:
        name: prod-nvme
        type: dir
        path: /var/lib/virtomir/images
        autostart: true
        state: active

    - name: 3. Provision tenant project with limits
      virtomir_project:
        id: prod-apps
        name: "Production Applications"
        members:
          - kind: group
            name: sysadmins
            role: admin
        limits:
          machines: 10
          vcpus: 32
          memory_mib: 65536
          disk_gib: 1000
        storage_pools: [prod-nvme]
        networks: [prod-vpc]
        state: present

    - name: 4. Deploy primary and secondary VMs
      machine:
        name: "{{ item }}"
        template: debian-13-cloud
        linked: true
        vcpus: 2
        memory_mb: 4096
        pool: prod-nvme
        network: prod-vpc
        state: running
      loop:
        - prod-app-01
        - prod-app-02

    - name: 5. Configure anti-affinity rule across hosts
      placement_rule:
        name: prod-app-spread
        kind: vm_anti_affinity
        strength: must
        domains: [prod-app-01, prod-app-02]
        enabled: true
        state: present

    - name: 6. Take baseline snapshot
      snapshot:
        machine: prod-app-01
        name: baseline-v1
        description: "Post-deployment verification"
        state: present
```

---

## Testing

Run the module unit test suite verifying check mode, idempotency, and API interactions:

```bash
python -m unittest integrations/ansible/tests/unit/test_modules.py
```

## License

GNU General Public License v3.0 or later (GPL-3.0-or-later). See `LICENSE` for details.
