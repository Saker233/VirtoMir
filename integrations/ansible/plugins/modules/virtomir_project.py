#!/usr/bin/python
# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function

__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_project
short_description: Manage multi-tenant projects in VirtoMir
description:
  - Create, update, or delete multi-tenant projects with members, quotas/limits, allowed storage pools, and networks (ADR-0060).
  - Idempotent update: compares existing members, limits, pools, and networks, calling the API only when changes exist.
  - Supports check mode.
version_added: "0.8.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
  id:
    description: Project ID slug (e.g. engineering, team-alpha). Lowercase letters, digits, and hyphens.
    type: str
    required: true
  name:
    description: Human-readable project name.
    type: str
    required: true
  description:
    description: Project description.
    type: str
    default: ''
  members:
    description: List of member specifications with kind, name, and role.
    type: list
    elements: dict
    suboptions:
      kind:
        description: Member type.
        type: str
        required: true
        choices: [user, group]
      name:
        description: Username or Linux/OIDC group name.
        type: str
        required: true
      role:
        description: Role inside this project.
        type: str
        required: true
        choices: [viewer, operator, admin]
    default: []
  limits:
    description: Resource limits/quotas for the project. 0 indicates unlimited.
    type: dict
    suboptions:
      machines:
        description: Maximum VM count.
        type: int
        default: 0
      vcpus:
        description: Maximum total vCPUs.
        type: int
        default: 0
      memory_mib:
        description: Maximum total RAM in MiB.
        type: int
        default: 0
      disk_gib:
        description: Maximum total disk space in GiB.
        type: int
        default: 0
    default: {}
  storage_pools:
    description: List of storage pool names allowed for this project.
    type: list
    elements: str
    default: []
  networks:
    description: List of virtual network names allowed for this project.
    type: list
    elements: str
    default: []
  state:
    description: Desired project state.
    type: str
    choices: [present, absent]
    default: present
  url:
    description: VirtoMir address (or VIRTOMIR_URL).
    type: str
  token:
    description: API token (or VIRTOMIR_TOKEN).
    type: str
  validate_certs:
    description: Check the host's certificate.
    type: bool
    default: true
  host:
    description: Cluster member to manage through url.
    type: str
'''

EXAMPLES = r'''
- name: Create or update development project
  onurbaydas.virtomir.virtomir_project:
    id: dev-team
    name: "Development Team"
    description: "Core engineering sandbox"
    members:
      - kind: user
        name: alice
        role: admin
      - kind: group
        name: devs
        role: operator
    limits:
      machines: 10
      vcpus: 40
      memory_mib: 65536
      disk_gib: 500
    storage_pools: [default, nvme-pool]
    networks: [default, dev-isolated]
    state: present

- name: Delete project
  onurbaydas.virtomir.project:
    id: dev-team
    name: "Development Team"
    state: absent
'''

RETURN = r'''
project:
  description: The project details as returned by the VirtoMir API.
  returned: when state == present
  type: dict
diff:
  description: Differences between previous and desired state.
  returned: always
  type: dict
'''

from ansible.module_utils.basic import AnsibleModule

try:
    from ansible_collections.onurbaydas.virtomir.plugins.module_utils.virtomir import (
        APIError, Client, common_args
    )
except ImportError:
    from ..module_utils.virtomir import APIError, Client, common_args


def norm_members(members):
    out = []
    for m in (members or []):
        if not m:
            continue
        out.append({
            'kind': m.get('kind', 'user'),
            'name': (m.get('name') or '').strip(),
            'role': m.get('role', 'viewer'),
        })
    return sorted(out, key=lambda x: (x['kind'], x['name']))


def norm_list(items):
    return sorted([str(x).strip() for x in (items or []) if str(x).strip()])


def norm_limits(limits):
    if not isinstance(limits, dict):
        limits = {}
    return {
        'machines': int(limits.get('machines') or 0),
        'vcpus': int(limits.get('vcpus') or 0),
        'memoryMiB': int(limits.get('memory_mib') or limits.get('memoryMiB') or 0),
        'diskGiB': int(limits.get('disk_gib') or limits.get('diskGiB') or 0),
    }


def main():
    member_spec = dict(
        kind=dict(type='str', required=True, choices=['user', 'group']),
        name=dict(type='str', required=True),
        role=dict(type='str', required=True, choices=['viewer', 'operator', 'admin']),
    )
    limits_spec = dict(
        machines=dict(type='int', default=0),
        vcpus=dict(type='int', default=0),
        memory_mib=dict(type='int', default=0),
        disk_gib=dict(type='int', default=0),
    )
    args = common_args()
    args.update(
        id=dict(type='str', required=True),
        name=dict(type='str', required=True),
        description=dict(type='str', default=''),
        members=dict(type='list', elements='dict', options=member_spec, default=[]),
        limits=dict(type='dict', options=limits_spec, default=dict(machines=0, vcpus=0, memory_mib=0, disk_gib=0)),
        storage_pools=dict(type='list', elements='str', default=[]),
        networks=dict(type='list', elements='str', default=[]),
        state=dict(type='str', choices=['present', 'absent'], default='present'),
    )
    module = AnsibleModule(argument_spec=args, supports_check_mode=True)
    p = module.params
    c = Client(module)

    pid = p['id'].strip()
    name = p['name'].strip()
    desc = (p.get('description') or '').strip()
    state = p['state']
    des_members = norm_members(p.get('members'))
    des_limits = norm_limits(p.get('limits'))
    des_pools = norm_list(p.get('storage_pools'))
    des_nets = norm_list(p.get('networks'))

    changed = False
    diff = {'before': {}, 'after': {}}

    try:
        current = c.project(pid)

        if state == 'absent':
            if current is not None:
                changed = True
                diff['before'] = current
                diff['after'] = {'state': 'absent'}
                if not module.check_mode:
                    c.delete_project(pid)
            module.exit_json(changed=changed, diff=diff)

        # Desired payload for create/update
        desired_payload = {
            'id': pid,
            'name': name,
            'description': desc,
            'members': des_members,
            'limits': des_limits,
            'storagePools': des_pools,
            'networks': des_nets,
        }

        if current is None:
            changed = True
            diff['before'] = {}
            diff['after'] = desired_payload
            if module.check_mode:
                module.exit_json(changed=True, diff=diff, project=None)
            created = c.create_project(desired_payload)
            module.exit_json(changed=True, diff=diff, project=created)

        # Compare existing vs desired
        cur_name = current.get('name', '').strip()
        cur_desc = (current.get('description') or '').strip()
        cur_members = norm_members(current.get('members'))
        cur_limits = norm_limits(current.get('limits'))
        cur_pools = norm_list(current.get('storagePools'))
        cur_nets = norm_list(current.get('networks'))

        diffs = {}
        if cur_name != name:
            diffs['name'] = {'before': cur_name, 'after': name}
        if cur_desc != desc:
            diffs['description'] = {'before': cur_desc, 'after': desc}
        if cur_members != des_members:
            diffs['members'] = {'before': cur_members, 'after': des_members}
        if cur_limits != des_limits:
            diffs['limits'] = {'before': cur_limits, 'after': des_limits}
        if cur_pools != des_pools:
            diffs['storagePools'] = {'before': cur_pools, 'after': des_pools}
        if cur_nets != des_nets:
            diffs['networks'] = {'before': cur_nets, 'after': des_nets}

        if diffs:
            changed = True
            diff['before'] = {k: v['before'] for k, v in diffs.items()}
            diff['after'] = {k: v['after'] for k, v in diffs.items()}
            if not module.check_mode:
                updated = c.update_project(pid, desired_payload)
                current = updated or c.project(pid)

        module.exit_json(changed=changed, diff=diff, project=current)

    except APIError as e:
        module.fail_json(msg=str(e), status=e.status)


if __name__ == '__main__':
    main()
